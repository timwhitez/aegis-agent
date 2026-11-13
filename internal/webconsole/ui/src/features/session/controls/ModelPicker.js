// Provider / model override popover for start and continue (default: inherit).
// Options come from /api/meta providers; the free-text model is optional.
// Placement (styles/composer.css): the picker root is static, so the popover
// is positioned against the composer toolbar and can never run past the
// composer edge. Wide screens start it under the chip (--model-popover-left,
// measured here on open and on resize) and CSS clamps it to the toolbar
// width; at ≤768px it spans the toolbar. Test ids: model-picker,
// model-popover, model-option, model-input, model-reset, model-done.
import { html, useEffect, useId, useRef, useState } from '../../../lib/html.js';
import { t } from '../../../lib/i18n.js';
import { setComposerModel } from '../../../state/actions.js';
import { Icon } from '../../../ui/Icon.js';
import { Button } from '../../../ui/Button.js';

export function ModelPicker({ provider = '', model = '', providers = [], defaultProvider = '' }) {
  const [open, setOpen] = useState(false);
  const [anchorLeft, setAnchorLeft] = useState(0);
  const rootRef = useRef(null);
  const triggerRef = useRef(null);
  const popoverRef = useRef(null);
  const id = useId();
  const list = (Array.isArray(providers) ? providers : []).filter((item) => item && item.name);
  const selected = list.find((item) => item.name === provider) || null;
  const summary = [provider, model].filter(Boolean).join(' · ');

  const close = (restore = true) => {
    setOpen(false);
    if (restore) triggerRef.current?.focus();
  };

  /** Chip offset inside the popover's containing block (the root's offsetParent). */
  const measureAnchor = () => {
    const trigger = triggerRef.current;
    const frame = rootRef.current?.offsetParent;
    if (!trigger || !frame) return 0;
    return Math.max(0, Math.round(trigger.getBoundingClientRect().left - frame.getBoundingClientRect().left - frame.clientLeft));
  };

  const toggle = () => {
    if (!open) setAnchorLeft(measureAnchor());
    setOpen(!open);
  };

  useEffect(() => {
    if (!open) return undefined;
    const target = popoverRef.current?.querySelector('input[type="radio"]:checked') || popoverRef.current?.querySelector('input');
    target?.focus();
    const onPointerDown = (event) => {
      if (!rootRef.current?.contains(event.target)) setOpen(false);
    };
    const onResize = () => setAnchorLeft(measureAnchor());
    document.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('resize', onResize);
    };
  }, [open]);

  const onKeyDown = (event) => {
    if (event.key === 'Escape' && open) {
      event.preventDefault();
      event.stopPropagation();
      close(true);
    }
  };

  return html`<div class="model-picker" ref=${rootRef} onKeyDown=${onKeyDown}>
    <button
      type="button"
      ref=${triggerRef}
      class=${`mode-chip model-chip ${summary ? 'is-set' : ''}`}
      aria-haspopup="dialog"
      aria-expanded=${String(open)}
      aria-controls=${open ? id : undefined}
      title=${t('composer.model.hint')}
      data-testid="model-picker"
      onClick=${toggle}
    >
      <${Icon} name="sparkles" size=${14} />
      ${summary ? html`<span class="mode-chip-label" translate="no">${summary}</span>` : html`<span class="mode-chip-label">${t('composer.model.default')}</span>`}
      <${Icon} name="chevron-down" size=${12} />
    </button>
    ${open ? html`<div
      class="model-popover"
      id=${id}
      role="dialog"
      aria-label=${t('composer.model.title')}
      ref=${popoverRef}
      style=${`--model-popover-left:${anchorLeft}px`}
      data-testid="model-popover"
    >
      <fieldset class="model-options">
        <legend class="model-popover-title">${t('composer.model.title')}</legend>
        <label class="model-option">
          <input type="radio" name=${`${id}-provider`} checked=${!provider} onChange=${() => setComposerModel({ provider: '', model })} data-testid="model-option" data-provider="" />
          <span class="model-option-text">
            <span class="model-option-name">${t('composer.model.default')}</span>
            <span class="model-option-desc">${t('composer.model.defaultHint')}</span>
          </span>
        </label>
        ${list.map((item) => html`<label class="model-option" key=${item.name}>
          <input type="radio" name=${`${id}-provider`} checked=${provider === item.name} onChange=${() => setComposerModel({ provider: item.name, model: '' })} data-testid="model-option" data-provider=${item.name} />
          <span class="model-option-text">
            <span class="model-option-name" translate="no">${item.name}</span>
            ${item.model ? html`<span class="model-option-desc" translate="no">${item.model}</span>` : null}
          </span>
          ${item.name === defaultProvider ? html`<span class="badge model-option-badge">${t('composer.model.isDefault')}</span>` : null}
        </label>`)}
      </fieldset>
      <label class="field model-custom">
        <span class="field-label">${t('composer.model.custom')}</span>
        <input
          class="input"
          value=${model}
          placeholder=${selected?.model || t('composer.model.customPlaceholder')}
          translate="no"
          autocomplete="off"
          spellcheck="false"
          data-testid="model-input"
          onInput=${(event) => setComposerModel({ provider, model: event.currentTarget.value })}
          onKeyDown=${(event) => { if (event.key === 'Enter') { event.preventDefault(); close(true); } }}
        />
      </label>
      <div class="model-popover-footer">
        <${Button} size="sm" variant="ghost" disabled=${!summary} data-testid="model-reset" onClick=${() => setComposerModel({})}>${t('composer.model.reset')}<//>
        <${Button} size="sm" variant="secondary" data-testid="model-done" onClick=${() => close(true)}>${t('composer.model.done')}<//>
      </div>
    </div>` : null}
  </div>`;
}
