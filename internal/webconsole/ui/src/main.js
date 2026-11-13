// Bootstrap: locale, dictionaries, router, live relay, polling, shortcuts, <App>.
import './i18n/index.js';
import { html, render } from './lib/html.js';
import { getLocale, initLocale, subscribeLocale } from './lib/i18n.js';
import { store, getState, update } from './state/store.js';
import { startRouter } from './state/router.js';
import { createPoller } from './state/polling.js';
import { createRelay } from './state/relay.js';
import {
  applyRelayFrame,
  refreshMeta,
  refreshOverview,
  refreshSession,
  reportMalformedRelayFrame,
  setOnline,
  setPageVisible,
  setRelayStatus,
  setViewport,
  syncSessionForRoute,
} from './state/actions.js';
import { installGlobalShortcuts } from './shell/shortcuts.js';
import { App } from './shell/App.js';

initLocale();
update('ui', { locale: getLocale() });
subscribeLocale((locale) => update('ui', { locale }));

const media = {
  narrow: globalThis.matchMedia('(max-width: 1024px)'),
  wide: globalThis.matchMedia('(min-width: 1280px)'),
  mobile: globalThis.matchMedia('(max-width: 768px)'),
};
const syncViewport = () => setViewport({ narrow: media.narrow.matches, wide: media.wide.matches, mobile: media.mobile.matches });
syncViewport();
Object.values(media).forEach((query) => query.addEventListener('change', syncViewport));

store.select((state) => state.route, (route) => syncSessionForRoute(route));
startRouter();
syncSessionForRoute(getState().route);

const relay = createRelay({
  url: `${globalThis.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${globalThis.location.host}/ws`,
  onStatus: setRelayStatus,
  onMessage: applyRelayFrame,
  onMalformed: reportMalformedRelayFrame,
  isHidden: () => document.hidden,
});

setPageVisible(!document.hidden);
document.addEventListener('visibilitychange', () => {
  setPageVisible(!document.hidden);
  if (document.hidden) {
    relay.pause();
    return;
  }
  relay.resume();
  refreshOverview();
  if (getState().session.id) refreshSession({ silent: true });
});
globalThis.addEventListener('online', () => setOnline(true));
globalThis.addEventListener('offline', () => setOnline(false));
setOnline(globalThis.navigator?.onLine !== false);

relay.connect();
createPoller({ getState, subscribe: store.subscribe, refreshSession, refreshOverview, timers: globalThis });
refreshMeta();
refreshOverview();
installGlobalShortcuts();

render(html`<${App} />`, document.getElementById('app'));
