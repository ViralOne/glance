/*
 * The Glance tracking snippet, readable source. glance.js is the minified
 * form that is actually served; `make snippet` regenerates it from this file.
 *
 * Design rules this file follows, because the snippet is the one piece of
 * Glance that runs on someone else's page:
 *
 *   - It never blocks. Everything happens after the page has loaded, and a
 *     failure is swallowed rather than thrown into the host page's console.
 *   - It sends no identifier of its own. No cookie, no localStorage entry, no
 *     device fingerprint. The visitor hash is derived server-side and rotates
 *     daily. The single exception is opt-in attribution, which stores a first
 *     referrer in the visitor's own browser and never sends it anywhere until
 *     the site itself chooses to.
 *   - It honours Do Not Track and Global Privacy Control before doing
 *     anything at all, including before reading the page URL.
 */
(function () {
  var d = document,
    s = d.currentScript,
    i = s && s.getAttribute("data-site"),
    debugEnabled = !!(s && s.hasAttribute("data-debug"));

  // Debugging is entirely local to the browser console: it adds no requests
  // and never prints the site id, endpoint, page URL, referrer, or event body.
  function debugLog(message, detail) {
    if (!debugEnabled) return;
    try {
      if (detail === undefined) console.debug("[Glance] " + message);
      else console.debug("[Glance] " + message, detail);
    } catch (e) {}
  }

  if (!i) {
    debugLog("disabled: missing data-site");
    return;
  }
  debugLog("starting");

  // Opting out is checked first and covers everything below: an opted-out
  // visitor is not measured, not sampled for vitals, and not given an
  // attribution record.
  var nav = navigator;
  if (nav.webdriver) {
    debugLog("disabled: webdriver");
    return;
  }
  if (nav.doNotTrack === "1") {
    debugLog("disabled: Do Not Track");
    return;
  }
  if (nav.globalPrivacyControl === true) {
    debugLog("disabled: Global Privacy Control");
    return;
  }

  // Where to post. The origin is taken from the script's own URL rather than
  // by stripping a known filename off it, because the script may be served
  // from any path the operator chose (see GLANCE_SNIPPET_PATH). The path below
  // is rewritten by the server when GLANCE_COLLECT_PATH is set, so a blocker
  // matching "/api/v1/collect" can be sidestepped without touching the page.
  var origin = s.getAttribute("data-api");
  if (!origin) {
    try {
      origin = new URL(s.src).origin;
    } catch (e) {
      debugLog("disabled: collector URL could not be resolved");
      return;
    }
  }
  var endpoint =
    origin.replace(/\/+$/, "") + (s.getAttribute("data-path") || "/api/v1/collect");
  debugLog("collector ready");

  // Vitals are opt-out rather than opt-in: they cost one PerformanceObserver
  // and describe the page rather than the person.
  var wantVitals = s.getAttribute("data-vitals") !== "false";

  // The reserved event name for a vitals-only flush; the server records the
  // measurements and no hit.
  var VITALS_ONLY = "$vitals";

  var lastURL;

  function send(body) {
    var json = JSON.stringify(body);
    debugLog("delivery attempt");
    try {
      fetch(endpoint, {
        method: "POST",
        body: json,
        keepalive: true,
        // A simple content type keeps this a CORS-safelisted request, so
        // there is no preflight on the collector.
        headers: { "Content-Type": "text/plain" },
        mode: "cors",
        credentials: "omit",
      }).then(
        function (response) {
          debugLog("collector responded", response.status);
          if (!response.ok) beacon();
        },
        function () {
          debugLog("fetch failed; trying beacon");
          beacon();
        }
      );
      return;
    } catch (e) {
      debugLog("fetch threw; trying beacon");
    }
    beacon();

    function beacon() {
      try {
        if (!nav.sendBeacon) {
          debugLog("sendBeacon unavailable");
          return;
        }
        debugLog(nav.sendBeacon(endpoint, json) ? "beacon queued" : "beacon rejected");
      } catch (e) {
        debugLog("beacon failed");
      }
    }
  }

  function base(name) {
    return {
      s: i,
      n: name,
      u: location.href,
      r: d.referrer,
      w: screen.width,
      tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
    };
  }

  function pageview() {
    var url = location.href;
    // A single-page app fires pushState for things that are not navigations;
    // the same URL twice in a row is not a second pageview.
    if (url === lastURL) return;
    lastURL = url;
    send(base("pageview"));
  }

  // ---- custom events ----

  // glance(name) or glance(name, props) or glance(name, props, valueInCents).
  var glance = (window.glance = function (name, props, value) {
    if (!name || name === "pageview") return;
    var body = base(String(name).slice(0, 60));
    if (props && typeof props === "object") body.x = props;
    if (typeof value === "number" && isFinite(value)) body.v = Math.round(value);
    send(body);
  });

  // ---- opt-in first-touch attribution ----

  var ATTR = "glance_attr";
  // 90 days, matching the window most checkouts assume.
  var ATTR_TTL = 7776e6;

  glance.attribution = function () {
    try {
      return JSON.parse(localStorage.getItem(ATTR));
    } catch (e) {
      return null;
    }
  };

  if (s.hasAttribute("data-attribution")) {
    try {
      var existing = glance.attribution();
      // First touch wins: only write when there is nothing, or what is there
      // has expired. This value stays in the visitor's browser and is never
      // sent to Glance; the site reads it and passes it to its own checkout.
      if (!existing || existing.t < Date.now() - ATTR_TTL) {
        localStorage.setItem(
          ATTR,
          JSON.stringify({ r: d.referrer, l: location.href, t: Date.now() })
        );
      }
    } catch (e) {}
  }

  // ---- Core Web Vitals ----

  // Collected with PerformanceObserver, which is the same source Chrome's own
  // field data uses. CLS is scaled by 1000 so every metric is one integer
  // column server-side; the server divides again when it renders.
  var vitals = {};
  var vitalsSent = false;
  // The page the vitals belong to. LCP, FCP and TTFB describe the document
  // that was loaded, so in a single-page app they stay attributed to the URL
  // that was actually fetched rather than to whatever route is showing when
  // the tab is finally closed.
  var vitalsPath = location.href;

  // Vitals are reported once, when the page goes away, and never alongside a
  // pageview.
  //
  // The tempting alternative — attach whatever is known to the next pageview —
  // does not work, and failing quietly is worse than not doing it. TTFB is
  // readable the instant the script runs, while LCP is not final until the
  // largest element has painted and CLS accumulates for the whole visit. So
  // the first pageview would carry TTFB alone, mark the batch as sent, and
  // every other metric would be discarded.
  function takeVitals() {
    if (!wantVitals || vitalsSent) return null;
    var out = null;
    for (var k in vitals) {
      if (!out) out = {};
      out[k] = Math.round(vitals[k]);
    }
    if (out) vitalsSent = true;
    return out;
  }

  // Observers are held for the lifetime of the page. The spec says a
  // registered observer stays reachable, so this is belt and braces rather
  // than a fix for an observed failure — but it is one line, and the failure
  // it would guard against is silent.
  var observers = [];

  function observe(type, handler, opts) {
    try {
      var po = new PerformanceObserver(function (list) {
        list.getEntries().forEach(handler);
      });
      po.observe(Object.assign({ type: type, buffered: true }, opts || {}));
      observers.push(po);
    } catch (e) {
      // An unsupported entry type throws; the metric is simply absent.
    }
  }

  if (wantVitals && window.PerformanceObserver) {
    // TTFB and FCP come from the navigation and paint timings.
    try {
      var navEntry = performance.getEntriesByType("navigation")[0];
      if (navEntry && navEntry.responseStart > 0) {
        vitals.ttfb = navEntry.responseStart;
      }
    } catch (e) {}
    observe("paint", function (e) {
      if (e.name === "first-contentful-paint") vitals.fcp = e.startTime;
    });

    // LCP keeps changing until the user interacts or the page is hidden; the
    // last value observed is the real one.
    observe("largest-contentful-paint", function (e) {
      vitals.lcp = e.startTime;
    });

    // INP is the worst interaction latency, near enough: the true metric is a
    // high percentile of interactions, which needs more bookkeeping than a
    // 2 KB script should carry. The maximum is the honest simplification —
    // it can only overstate, never flatter.
    observe(
      "event",
      function (e) {
        if (e.interactionId && (!vitals.inp || e.duration > vitals.inp)) {
          vitals.inp = e.duration;
        }
      },
      { durationThreshold: 40 }
    );

    // CLS sums layout shifts that were not caused by recent input.
    var cls = 0;
    observe("layout-shift", function (e) {
      if (!e.hadRecentInput) {
        cls += e.value;
        vitals.cls = cls * 1000;
      }
    });
  }

  // ---- lifecycle ----

  var h = history,
    push = h.pushState;
  if (push) {
    h.pushState = function () {
      push.apply(h, arguments);
      // The URL changes before the framework has rendered; a tick later the
      // page is what the visitor will actually see.
      setTimeout(pageview, 0);
    };
  }
  addEventListener("popstate", pageview);

  // Both events are needed and they are *not* interchangeable.
  // visibilitychange is the one that fires when a mobile user switches apps,
  // where pagehide may never come; pagehide is the one that fires on an
  // ordinary navigation, where the page can still be visible at the time. An
  // earlier version gated both on visibilityState === "hidden", which quietly
  // made pagehide a no-op and lost every metric except TTFB on desktop.
  // takeVitals only yields once, so whichever fires first wins.
  function flush() {
    var v = takeVitals();
    if (!v) return;
    var body = base(VITALS_ONLY);
    body.u = vitalsPath;
    body.cwv = v;
    send(body);
  }
  addEventListener("visibilitychange", function () {
    if (d.visibilityState === "hidden") flush();
  });
  addEventListener("pagehide", flush);

  pageview();
})();
