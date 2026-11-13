// Cross-cutting audits run on every screenshot state (and helpers for
// dialog focus behaviour). All checks run inside the page and return
// violation lists; the harness turns non-empty lists into failed checks.
import assert from 'node:assert/strict';

/**
 * Brand / product names, units and key names that may stay Latin in zh-CN.
 * Every entry corresponds to a deliberate zh-CN dictionary value or a unit.
 */
const ZH_LATIN_ALLOWLIST = [
  '^Aegis( Agent)?$',
  '^(Goal|Plan|Token|Top P|Google Gemini|Base URL|API Key|EN|Switch to English)$',
  '^(PID|ID|API|URL|JSON|HTTP|CLI|UTF-8|CRLF|LF)$',
  '^v\\d+$',
  '^Token [\\d.,]+[kKmM]?( / [\\d.,]+[kKmM]?)?$',
  '^[\\d.,\\s]+(B|KB|MB|GB|KiB|MiB|GiB|TiB|ms|s|k|K|M)$',
];

export async function auditScreen(page, { allowLatin = [] } = {}) {
  return page.evaluate(({ allow }) => {
    const allowed = allow.map((source) => new RegExp(source));
    const lang = document.documentElement.lang;
    const width = window.innerWidth;
    const coarse = matchMedia('(pointer: coarse)').matches;
    const minTarget = coarse || width <= 768 ? 44 : 32;
    const RAW = '[translate="no"], pre, code, kbd, samp, script, style, noscript, template';
    const han = /\p{Script=Han}/u;
    const shown = (element) => Boolean(element?.isConnected && element.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })
      && element.getClientRects().length);
    const describe = (element) => {
      const id = element.getAttribute('data-testid') || element.id || '';
      const cls = typeof element.className === 'string' ? element.className.split(/\s+/).filter(Boolean).slice(0, 3).join('.') : '';
      return element.tagName.toLowerCase() + (id ? '[' + id + ']' : '') + (cls ? '.' + cls : '');
    };
    const result = { i18n: [], translateYes: [], hitTargets: [], overflow: [], lang: [] };

    if (!['zh-CN', 'en'].includes(lang)) result.lang.push('html lang=' + lang);

    // ---- i18n: visible operator text and accessible names --------------------------
    const flag = (text, where) => {
      const value = text.replace(/\s+/g, ' ').trim();
      if (!value) return;
      if (lang === 'zh-CN') {
        if (han.test(value) || !/[A-Za-z]{2,}/.test(value)) return;
        if (allowed.some((pattern) => pattern.test(value))) return;
        result.i18n.push(where + ': ' + value.slice(0, 120));
      } else if (lang === 'en' && han.test(value)) {
        result.i18n.push(where + ': ' + value.slice(0, 120));
      }
    };
    const otherLang = lang === 'zh-CN' ? '[lang="en"]' : '[lang="zh-CN"]';
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) {
      const node = walker.currentNode;
      const parent = node.parentElement;
      if (!parent || !node.nodeValue.trim() || parent.closest(RAW) || parent.closest(otherLang)) continue;
      if (!shown(parent)) continue;
      flag(node.nodeValue, describe(parent));
    }
    for (const element of document.querySelectorAll('[aria-label], [title], [placeholder], [alt], [aria-description]')) {
      if (element.closest(RAW) || element.closest(otherLang) || !shown(element)) continue;
      for (const name of ['aria-label', 'title', 'placeholder', 'alt', 'aria-description']) {
        const value = (element.getAttribute(name) || '').trim();
        // Tooltips that repeat a raw path / id / URL verbatim are durable values, not copy.
        if (!value || (/^\S+$/.test(value) && /[/\\_:.@0-9]/.test(value))) continue;
        flag(value, describe(element) + '@' + name);
      }
    }

    // ---- raw durable values never opt back into translation ------------------------
    for (const element of document.querySelectorAll('[translate="yes"]')) result.translateYes.push(describe(element));

    // ---- hit targets ------------------------------------------------------------------
    const targets = document.querySelectorAll('button, [role="button"], [role="tab"], [role="menuitem"], [role="option"], a[href], summary');
    for (const element of targets) {
      if (!shown(element) || element.closest('[inert]') || element.disabled || element.getAttribute('aria-disabled') === 'true') continue;
      if (element.closest('.skip-link, .md, .msg-bubble, .insp-markdown, [data-hit-exempt]')) continue;
      // WCAG 2.5.8 inline exception: an underlined text link inside a sentence.
      const style = getComputedStyle(element);
      if (['inline', 'inline-block'].includes(style.display) && style.textDecorationLine.includes('underline') && element.parentElement
        && /\S/.test((element.parentElement.textContent || '').replace(element.textContent || '', ''))) continue;
      const rect = element.getBoundingClientRect();
      if (rect.width < minTarget - 0.5 || rect.height < minTarget - 0.5) {
        result.hitTargets.push(describe(element) + ' ' + Math.round(rect.width) + 'x' + Math.round(rect.height) + ' <' + minTarget
          + ' "' + (element.getAttribute('aria-label') || element.textContent || '').trim().slice(0, 40) + '"');
      }
    }

    // ---- horizontal overflow on narrow viewports ------------------------------------
    if (width <= 400) {
      const roots = [document.documentElement, document.body, ...document.querySelectorAll('.session-scroll, .page, .app-main, .app-view')];
      for (const element of roots) {
        const extra = element.scrollWidth - element.clientWidth;
        if (extra > 1) result.overflow.push(describe(element) + ' +' + extra + 'px');
      }
    }
    for (const key of Object.keys(result)) result[key] = Array.from(new Set(result[key])).slice(0, 20);
    return result;
  }, { allow: [...ZH_LATIN_ALLOWLIST, ...allowLatin] });
}

/**
 * Dialog contract: focus starts inside, Tab / Shift+Tab stay inside, the
 * layout is inert, Esc closes, inert is released and focus returns to the
 * element that opened it (when returnTo is given).
 */
export async function assertDialogContract(page, dialog, { returnTo, closeWith = 'Escape', inert = true } = {}) {
  await dialog.waitFor();
  const inside = () => dialog.evaluate((node) => node.contains(document.activeElement));
  await page.waitForFunction((node) => node.contains(document.activeElement), await dialog.elementHandle());
  if (inert) assert.equal(await page.locator('#app-layout').evaluate((node) => node.inert), true, 'layout is inert while the dialog is open');
  const focusable = await dialog.evaluate((node) => node.querySelectorAll('a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])').length);
  for (let index = 0; index < Math.min(focusable + 1, 14); index += 1) {
    await page.keyboard.press('Tab');
    assert.equal(await inside(), true, 'Tab keeps focus inside the dialog');
  }
  await page.keyboard.press('Shift+Tab');
  assert.equal(await inside(), true, 'Shift+Tab keeps focus inside the dialog');
  if (!closeWith) return;
  await page.keyboard.press(closeWith);
  await dialog.waitFor({ state: 'detached' });
  if (inert) await page.waitForFunction(() => !document.getElementById('app-layout')?.inert);
  if (returnTo) {
    await page.waitForFunction((node) => document.activeElement === node, await returnTo.elementHandle());
  }
}
