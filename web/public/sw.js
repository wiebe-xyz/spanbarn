const CACHE_NAME = 'spanbarn-v2';
const API_CACHE = 'spanbarn-api-v1';
const STATIC_ASSETS = [
  '/',
  '/favicon.svg',
  '/manifest.json',
];

const API_SWR_MAX_AGE_MS = 30_000;

const PAGE_API_MAP = {
  '/': ['/api/v1/services'],
  '/services': ['/api/v1/services'],
  '/dependencies': ['/api/v1/dependencies'],
  '/database': ['/api/v1/database'],
  '/pages': ['/api/v1/web-vitals'],
  '/service-map': ['/api/v1/service-map'],
  '/prompts': ['/api/v1/prompts'],
};

const NAV_ADJACENCY = {
  '/': ['/dependencies', '/pages'],
  '/services': ['/dependencies', '/pages'],
  '/dependencies': ['/services', '/database'],
  '/database': ['/dependencies', '/prompts'],
  '/pages': ['/services'],
  '/service-map': ['/services', '/dependencies'],
  '/prompts': ['/database'],
};

function defaultTimeRange() {
  const to = new Date().toISOString();
  const from = new Date(Date.now() - 3600_000).toISOString();
  return `from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`;
}

function prefetchForPages(pages) {
  const timeQs = defaultTimeRange();
  const urls = [];
  for (const page of pages) {
    const endpoints = PAGE_API_MAP[page];
    if (endpoints) {
      for (const ep of endpoints) {
        urls.push(`${ep}?${timeQs}`);
      }
    }
  }
  if (urls.length === 0) return;

  caches.open(API_CACHE).then((cache) => {
    for (const url of urls) {
      fetch(url, { credentials: 'same-origin' })
        .then((res) => {
          if (res.ok) {
            const headers = new Headers(res.headers);
            headers.set('x-sw-cached-at', String(Date.now()));
            const cachedRes = new Response(res.body, {
              status: res.status,
              statusText: res.statusText,
              headers,
            });
            cache.put(new Request(url), cachedRes);
          }
        })
        .catch(() => {});
    }
  });
}

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(STATIC_ASSETS))
  );
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(
        keys
          .filter((k) => k !== CACHE_NAME && k !== API_CACHE)
          .map((k) => caches.delete(k))
      )
    )
  );
  self.clients.claim();
});

self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'PREFETCH_ADJACENT') {
    const currentPath = event.data.path || '/';
    const adjacent = NAV_ADJACENCY[currentPath] || [];
    prefetchForPages(adjacent);
  }
});

self.addEventListener('fetch', (event) => {
  const { request } = event;
  const url = new URL(request.url);

  if (url.pathname.startsWith('/api/') && request.method === 'GET') {
    if (url.pathname === '/api/v1/health' || url.pathname.includes('/login') || url.pathname.includes('/logout')) {
      return;
    }

    event.respondWith(
      caches.open(API_CACHE).then((cache) =>
        cache.match(request).then((cached) => {
          const networkFetch = fetch(request).then((response) => {
            if (response.ok) {
              const headers = new Headers(response.headers);
              headers.set('x-sw-cached-at', String(Date.now()));
              const toCache = new Response(response.clone().body, {
                status: response.status,
                statusText: response.statusText,
                headers,
              });
              cache.put(request, toCache);
            }
            return response;
          });

          if (cached) {
            const cachedAt = Number(cached.headers.get('x-sw-cached-at') || 0);
            if (Date.now() - cachedAt < API_SWR_MAX_AGE_MS) {
              networkFetch.catch(() => {});
              return cached;
            }
          }

          return networkFetch;
        })
      )
    );
    return;
  }

  if (url.pathname.startsWith('/v1/')) {
    return;
  }

  if (request.mode === 'navigate') {
    event.respondWith(
      fetch(request)
        .then((response) => {
          const clone = response.clone();
          caches.open(CACHE_NAME).then((cache) => cache.put(request, clone));
          return response;
        })
        .catch(() => caches.match(request).then((cached) => cached || caches.match('/')))
    );
    return;
  }

  if (
    url.pathname.match(/\.(js|css|png|jpg|jpeg|svg|woff2?|ttf|eot)$/) ||
    url.pathname.startsWith('/assets/')
  ) {
    event.respondWith(
      caches.match(request).then(
        (cached) =>
          cached ||
          fetch(request).then((response) => {
            const clone = response.clone();
            caches.open(CACHE_NAME).then((cache) => cache.put(request, clone));
            return response;
          })
      )
    );
    return;
  }
});
