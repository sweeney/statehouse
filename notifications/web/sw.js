// Service worker. On iOS/iPadOS 18.4+ Safari renders our Declarative Web Push
// payloads itself and this push handler never runs. Everywhere else (Chrome,
// Firefox, Android, older iOS) it does, and reads the same JSON.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('push', (event) => {
  let msg = {};
  try { msg = event.data ? event.data.json() : {}; } catch { /* not JSON */ }
  const n = msg.notification || { title: 'Notifications', body: event.data ? event.data.text() : '' };
  // Must always show something: a push with no notification counts against
  // the origin, and Safari revokes subscriptions that do it repeatedly.
  event.waitUntil(self.registration.showNotification(n.title || 'Notifications', {
    body: n.body || '',
    tag: n.tag || undefined,
    renotify: !!n.tag,
    lang: n.lang,
    icon: '/icon-512.png',
    data: { url: n.navigate || '/' },
  }));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const url = (event.notification.data && event.notification.data.url) || '/';
  event.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const w of wins) { if ('focus' in w) { await w.navigate(url).catch(() => {}); return w.focus(); } }
    return self.clients.openWindow(url);
  })());
});
