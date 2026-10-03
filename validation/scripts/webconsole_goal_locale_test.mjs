import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const assets = new URL('../../internal/webconsole/assets/', import.meta.url);
const document = { readyState: 'loading', documentElement: { setAttribute() {} }, addEventListener() {}, getElementById: () => null };
const window = { document, localStorage: { getItem: () => null, setItem() {} } };
const ctx = { window, document,
  humanizeStatus: value => String(value || '').replaceAll('_', ' ').replace(/^./, ch => ch.toUpperCase()),
  toneForStatus: () => 'neutral', isMultiAgentTool: () => false };
vm.createContext(ctx);
for (const file of ['i18n.js', 'utils.js', 'session-view.js']) vm.runInContext(readFileSync(new URL(file, assets), 'utf8'), ctx, { filename: file });
const appSource = readFileSync(new URL('app.js', assets), 'utf8');
vm.runInContext(appSource.match(/function phaseHeadline\(phase\) \{[\s\S]*?\n\}/)[0], ctx, { filename: 'app.js:phaseHeadline' });

const summary = 'The approved local validation command completed with the expected output.';
const kind = 'progress';
const call = { id: 'call_progress', name: 'record_goal_progress', arguments: { kind, summary } };
const goal = { goal_id: 'goal_progress', mode: 'mission', status: 'active', tokens_used: 0,
  progress: [{ id: 'progress_raw', kind, summary }] };
const rawSpan = value => new RegExp(`<span[^>]*translate="no"[^>]*data-i18n-skip[^>]*>${value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}</span>`);

test('Goal progress fixed operator labels translate in zh-CN and preserve their English wording', () => {
  const cases = [['1 progress', '1 条进度'], ['Latest progress', '最新进度'], ['Recent progress', '最近进度'],
    ['Read current durable Goal state', '读取当前持久化目标状态'], ['Kind', '类型'], ['plan · Executing', '计划 · 执行中']];
  window.AegisI18n.setLocale('zh-CN');
  for (const [source, translated] of cases) assert.equal(window.AegisI18n.t(source), translated, source);
  window.AegisI18n.setLocale('en');
  for (const [source] of cases) assert.equal(window.AegisI18n.t(source), source, source);
});

function completedGoalLine(budgets = {}) {
  const html = ctx.renderSessionGoalLine({
    state: { status: 'completed', phase: 'turn_decide' },
    goal: { ...goal, status: 'complete', tokens_used: 12, provider_time_used_seconds: 6, ...budgets },
    goal_facts: { latest_history: { type: 'goal.accounting_updated', created_at: '2026-10-03T12:34:56Z' } }
  });
  return html.match(/<span>([^<]+)<\/span>/)?.[1] || '';
}

test('actual completed session Goal line translates all fixed labels and preserves accounting values', () => {
  window.AegisI18n.setLocale('zh-CN');
  for (const [budgets, tokens, providerTime] of [
    [{}, '12', '6s'],
    [{ token_budget: 100, provider_time_budget_seconds: 20 }, '12 / 100', '6s / 20s']
  ]) {
    const source = completedGoalLine(budgets);
    const date = ctx.formatTimestamp('2026-10-03T12:34:56Z');
    assert.equal(source, `session Completed · Awaiting model decision · tokens ${tokens} · provider time ${providerTime} · latest Goal accounting updated · ${date}`);
    assert.equal(window.AegisI18n.t(source), `会话已完成 · 等待模型决策 · Token ${tokens} · 提供商耗时 ${providerTime} · 最近目标记账更新 · ${date}`);
  }
});

test('actual completed session Goal line retains its original English labels and accounting values', () => {
  window.AegisI18n.setLocale('en');
  const source = completedGoalLine({ token_budget: 100, provider_time_budget_seconds: 20 });
  assert.match(source, /^session Completed · Awaiting model decision · tokens 12 \/ 100 · provider time 6s \/ 20s · latest Goal accounting updated · /);
  assert.equal(window.AegisI18n.t(source), source);
});

test('actual progress tool call preview keeps arbitrary kind and summary in raw descendants', () => {
  const html = ctx.renderToolCall(call);
  assert.match(html, rawSpan(kind));
  assert.match(html, rawSpan(summary));
  assert.doesNotMatch(html, /class="tool-card-summary-copy"[^>]*translate="no"/);
  assert.match(html, /class="tool-card-type">Call<\/span>/);
});

test('actual main tool lane progress call preview preserves raw facts while its operator labels remain audited', () => {
  const html = ctx.renderToolLane({ tool_calls: [call] });
  const preview = html.split('<summary class="tl-summary">')[1]?.split('</summary>')[0] || '';
  assert.match(preview, rawSpan(kind));
  assert.match(preview, rawSpan(summary));
  assert.doesNotMatch(preview, /class="tl-preview"[^>]*translate="no"/);
  assert.match(preview, /class="tl-type-chip call">Call<\/span>/);
  const readGoal = ctx.renderToolLane({ tool_calls: [{ name: 'get_goal', arguments: {} }] });
  assert.match(readGoal, /class="tl-preview">Read current durable Goal state<\/span>/);
});

test('actual progress call body separates raw summary and kind from the fixed kind label', () => {
  const html = ctx.renderGoalToolCallBody(call);
  assert.match(html, rawSpan(summary));
  assert.match(html, /<span>Kind<\/span> <span[^>]*translate="no"[^>]*>progress<\/span>/);
  assert.doesNotMatch(html, /class="surface-chip"[^>]*translate="no"/);
});

test('actual progress result preview and latest card preserve raw values without excluding operator labels', () => {
  const html = ctx.renderToolResult({ name: 'record_goal_progress', display_output: JSON.stringify(goal) });
  assert.match(html, rawSpan(summary));
  assert.match(html, rawSpan(kind));
  assert.match(html, /class="goal-section-title">Latest progress<\/span>/);
  assert.match(html, /class="surface-chip">1 progress<\/span>/);
  assert.doesNotMatch(html, /class="goal-tool-progress"[^>]*translate="no"/);
  const preview = html.match(/<div class="tool-card-summary-copy">([\s\S]*?)<\/div>/)?.[1] || '';
  assert.match(preview, rawSpan(summary));
});

test('actual main tool lane progress result audits fixed status and metrics while preserving raw facts', () => {
  const output = { ...goal, tokens_used: 7, token_budget: 100, provider_time_used_seconds: 3, provider_time_budget_seconds: 10 };
  const html = ctx.renderToolLane({ tool_calls: [call], tool_results: [{ tool_call_id: call.id,
    name: call.name, display_output: JSON.stringify(output) }] });
  const preview = html.split('<summary class="tl-summary">')[2]?.split('</summary>')[0] || '';
  assert.doesNotMatch(preview, /class="tl-preview"[^>]*translate="no"/);
  assert.match(preview, rawSpan(kind));
  assert.match(preview, rawSpan(summary));
  assert.match(preview, rawSpan('mission'));
  assert.match(preview, /<span>Active<\/span>/);
  assert.match(preview, /<span>tokens 7<\/span>/);
  assert.match(preview, /<span>provider time 3s<\/span>/);
  assert.match(html, /tokens 7 \/ 100/);
  assert.match(html, /provider time 3s \/ 10s/);
  const other = ctx.renderToolLaneResultRow({ name: 'get_goal', display_output: JSON.stringify(goal) }, false);
  assert.match(other, /class="tl-preview" translate="no" data-i18n-skip/);
});

test('operator-looking and HTML-like progress facts remain escaped raw values', () => {
  const html = ctx.renderGoalToolCallBody({ name: call.name, arguments: { kind: '<custom kind>', summary: 'Latest progress' } });
  assert.match(html, rawSpan('&lt;custom kind&gt;'));
  assert.match(html, rawSpan('Latest progress'));
  assert.doesNotMatch(html, /<custom kind>/);
});

test('locale switching preserves rendered dictionary-key and Chinese progress facts while translating the kind label', () => {
  for (const value of ['Settings', 'Progress', '原始进度']) {
    const html = ctx.renderGoalToolCallBody({ name: call.name, arguments: { kind: value, summary: value } });
    const leaves = [...html.matchAll(/<span([^>]*)>([^<]*)<\/span>/g)].map(([, attributes, text]) => {
      const parent = {
        nodeType: 1,
        closest: () => /translate="no"|data-i18n-skip/.test(attributes) ? parent : null,
        hasAttribute: name => new RegExp(`\\b${name}(?:=|\\s|$)`).test(attributes)
      };
      return { nodeType: 3, nodeValue: text, parentElement: parent };
    });
    const rawFacts = leaves.filter(node => node.nodeValue === value);
    const label = leaves.find(node => node.nodeValue === 'Kind');
    assert.ok(rawFacts.length >= 2, 'call body exposes kind and summary');
    assert.ok(label);
    window.AegisI18n.setLocale('zh-CN');
    for (const node of leaves) window.AegisI18n.apply(node);
    assert.equal(label.nodeValue, '类型');
    for (const node of rawFacts) assert.equal(node.nodeValue, value);
    window.AegisI18n.setLocale('en');
    for (const node of leaves) window.AegisI18n.apply(node);
    assert.equal(label.nodeValue, 'Kind');
    for (const node of rawFacts) assert.equal(node.nodeValue, value);
  }
});

test('recent goal facts keep kind and summary raw and retain a fixed progress heading', () => {
  const html = ctx.renderGoalFacts({ progress: goal.progress });
  assert.match(html, rawSpan(kind));
  assert.match(html, /class="goal-raw" translate="no" data-i18n-skip>/);
  assert.match(html, /class="goal-section-title sub">Recent progress<\/div>/);
});

test('missing progress facts retain translated operator fallbacks instead of raw model text', () => {
  const html = ctx.renderGoalToolCallBody({ name: 'record_goal_progress', arguments: {} });
  assert.match(html, /<span>Progress<\/span>/);
  assert.match(html, /<span>Record progress facts<\/span>/);
  assert.doesNotMatch(html, /translate="no"[^>]*>Record progress facts/);
});

test('get_goal action control remains a translatable operator label with no raw container', () => {
  const html = ctx.renderGoalToolCallBody({ name: 'get_goal', arguments: {} });
  assert.match(html, /class="goal-tool-title">Read current durable Goal state<\/span>/);
  assert.doesNotMatch(html, /class="goal-tool-title"[^>]*translate="no"/);
});
