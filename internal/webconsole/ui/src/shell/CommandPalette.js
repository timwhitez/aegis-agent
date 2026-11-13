// ⌘K command palette: combobox + listbox, fuzzy ranking from lib/commands.js.
import { html, useEffect, useMemo, useRef, useState } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { rankCommands, isCommandEnabled } from '../lib/commands.js';
import { getState } from '../state/store.js';
import { useStore } from '../state/hooks.js';
import { closePalette } from '../state/actions.js';
import { Dialog } from '../ui/Dialog.js';
import { Icon } from '../ui/Icon.js';
import { Kbd } from '../ui/Kbd.js';
import { buildShellCommands } from './palette-commands.js';

const GROUP_ORDER = ['actions', 'navigation', 'session', 'composer', 'view', 'recent'];

export function CommandPalette() {
  useStore((state) => [state.ui.locale, state.overview.sessions, state.session.detail, state.route.name], (a, b) => a.every((value, index) => Object.is(value, b[index])));
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const inputRef = useRef(null);
  const listRef = useRef(null);
  const { commands, recent } = buildShellCommands(getState());

  const results = useMemo(() => {
    const visible = commands.filter(isCommandEnabled);
    const ranked = query.trim()
      ? rankCommands([...visible, ...recent], query, { limit: 30 }).map((entry) => entry.command)
      : [...visible, ...recent.slice(0, 6)];
    const grouped = GROUP_ORDER.map((group) => ({ group, items: ranked.filter((command) => command.group === group) })).filter((entry) => entry.items.length);
    return { grouped, flat: grouped.flatMap((entry) => entry.items) };
  }, [query, commands.length, recent]);

  useEffect(() => setActive(0), [query]);
  useEffect(() => {
    listRef.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const run = (command) => {
    if (!command) return;
    closePalette();
    command.run();
  };
  const onKeyDown = (event) => {
    if (event.isComposing) return;
    const count = results.flat.length;
    if (event.key === 'ArrowDown') { event.preventDefault(); setActive(count ? (active + 1) % count : 0); }
    else if (event.key === 'ArrowUp') { event.preventDefault(); setActive(count ? (active - 1 + count) % count : 0); }
    else if (event.key === 'Home' && event.ctrlKey) { event.preventDefault(); setActive(0); }
    else if (event.key === 'Enter') { event.preventDefault(); run(results.flat[active]); }
  };

  const optionId = (command) => `palette-option-${command.id.replace(/[^a-zA-Z0-9_-]/g, '_')}`;
  const activeCommand = results.flat[active];
  return html`<${Dialog}
    title=${t('shell.palette.label')}
    hideTitle
    showClose=${false}
    placement="top"
    size="lg"
    class="command-palette"
    testId="command-palette"
    initialFocusRef=${inputRef}
    onClose=${closePalette}
  >
    <div class="palette-input-row">
      <${Icon} name="search" size=${18} />
      <input
        ref=${inputRef}
        class="palette-input"
        role="combobox"
        aria-expanded="true"
        aria-controls="palette-listbox"
        aria-autocomplete="list"
        aria-activedescendant=${activeCommand ? optionId(activeCommand) : undefined}
        aria-label=${t('shell.palette.label')}
        placeholder=${t('shell.palette.placeholder')}
        value=${query}
        data-testid="command-palette-input"
        onInput=${(event) => setQuery(event.currentTarget.value)}
        onKeyDown=${onKeyDown}
      />
    </div>
    <div class="palette-list" id="palette-listbox" role="listbox" aria-label=${t('shell.palette.label')} ref=${listRef}>
      ${results.flat.length === 0 ? html`<p class="palette-empty">${t('shell.palette.empty')}</p>` : null}
      ${results.grouped.map((entry) => html`<div role="group" aria-labelledby=${`palette-group-${entry.group}`} class="palette-group" key=${entry.group}>
        <div class="palette-group-label" id=${`palette-group-${entry.group}`} role="presentation">${t(`shell.palette.group.${entry.group}`)}</div>
        ${entry.items.map((command) => {
          const index = results.flat.indexOf(command);
          return html`<div
            key=${command.id}
            id=${optionId(command)}
            role="option"
            aria-selected=${String(index === active)}
            class=${`palette-option ${index === active ? 'is-active' : ''}`}
            data-command-id=${command.id}
            onMouseMove=${() => index !== active && setActive(index)}
            onClick=${() => run(command)}
          >
            <${Icon} name=${command.icon || 'command'} size=${16} />
            <span class="palette-option-title">${command.rawTitle ? html`<span translate="no">${command.title}</span>` : command.title}</span>
            ${command.group === 'recent' ? html`<span class="palette-option-meta" translate="no">${command.subtitle}</span>` : null}
            ${command.shortcut ? html`<${Kbd} keys=${command.shortcut} />` : null}
          </div>`;
        })}
      </div>`)}
    </div>
    <div class="palette-footer">${t('shell.palette.hint')}</div>
  <//>`;
}
