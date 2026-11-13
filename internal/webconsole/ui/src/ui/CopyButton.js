import { html, useEffect, useRef, useState } from '../lib/html.js';
import { t } from '../lib/i18n.js';
import { IconButton } from './IconButton.js';

export async function copyText(text) {
  try {
    if (globalThis.navigator?.clipboard?.writeText) {
      await globalThis.navigator.clipboard.writeText(String(text));
      return true;
    }
  } catch {
    // Fall through to the selection fallback (non-secure contexts).
  }
  try {
    const area = document.createElement('textarea');
    area.value = String(text);
    area.setAttribute('readonly', '');
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.appendChild(area);
    area.select();
    const ok = document.execCommand('copy');
    area.remove();
    return ok;
  } catch {
    return false;
  }
}

/** <CopyButton text label? size? onCopied?(ok) /> — icon swaps to a check for 1.5s. */
export function CopyButton({ text, label = '', size = 'sm', variant = 'ghost', onCopied, class: className = '' }) {
  const [state, setState] = useState('idle');
  const timer = useRef(null);
  useEffect(() => () => clearTimeout(timer.current), []);
  const onClick = async () => {
    const ok = await copyText(text);
    setState(ok ? 'copied' : 'failed');
    onCopied?.(ok);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setState('idle'), 1500);
  };
  const currentLabel = state === 'copied' ? t('common.copied') : state === 'failed' ? t('common.copyFailed') : (label || t('common.copy'));
  return html`<${IconButton} icon=${state === 'copied' ? 'check' : 'copy'} label=${currentLabel} size=${size} variant=${variant} class=${className} onClick=${onClick} />`;
}
