// agentcookie stealth content script.
//
// Injected into every page at document_start in the MAIN execution world,
// before any anti-bot detection script can read fingerprint signals. The
// patches here mirror the well-documented evasions from puppeteer-extra-
// plugin-stealth and rebrowser-patches.
//
// v0.4 ships the minimum viable patch set: navigator.webdriver, plugins,
// languages, chrome.runtime. WebGL/canvas fingerprint randomization lands
// in v0.5.

(function () {
  "use strict";

  // 1. navigator.webdriver: undefined when not under WebDriver.
  try {
    Object.defineProperty(Navigator.prototype, "webdriver", {
      get: () => undefined,
      configurable: true,
    });
  } catch {}

  // 2. navigator.plugins: real Chrome ships at least a few.
  try {
    if (navigator.plugins && navigator.plugins.length === 0) {
      const fakePlugins = [
        { name: "PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" },
        { name: "Chrome PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" },
        { name: "Chromium PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" },
      ];
      Object.defineProperty(Navigator.prototype, "plugins", {
        get: () => fakePlugins,
        configurable: true,
      });
    }
  } catch {}

  // 3. navigator.languages: must be a non-empty array.
  try {
    if (!navigator.languages || navigator.languages.length === 0) {
      Object.defineProperty(Navigator.prototype, "languages", {
        get: () => ["en-US", "en"],
        configurable: true,
      });
    }
  } catch {}

  // 4. window.chrome: provide a populated object so detectors that check
  //    !!window.chrome and chrome.runtime see realistic shape.
  try {
    if (!window.chrome) {
      window.chrome = {};
    }
    if (!window.chrome.runtime) {
      window.chrome.runtime = {
        id: undefined,
        OnInstalledReason: { INSTALL: "install", UPDATE: "update" },
        sendMessage: () => {},
        connect: () => ({ onMessage: { addListener: () => {} } }),
      };
    }
  } catch {}

  // 5. permissions.query: real Chrome returns "prompt" for Notifications when
  //    not granted; headless leaks "denied" for the page's origin even when
  //    the global is "default."
  try {
    const originalQuery = window.navigator.permissions && window.navigator.permissions.query;
    if (originalQuery) {
      window.navigator.permissions.query = (parameters) => {
        if (parameters && parameters.name === "notifications") {
          return Promise.resolve({ state: Notification.permission || "prompt" });
        }
        return originalQuery.call(window.navigator.permissions, parameters);
      };
    }
  } catch {}

  // 6. WebGL renderer + vendor: return common consumer values instead of
  //    Chrome's headless-flavored defaults.
  try {
    const getParameter = WebGLRenderingContext.prototype.getParameter;
    WebGLRenderingContext.prototype.getParameter = function (parameter) {
      // UNMASKED_VENDOR_WEBGL = 0x9245, UNMASKED_RENDERER_WEBGL = 0x9246
      if (parameter === 0x9245) return "Apple Inc.";
      if (parameter === 0x9246) return "Apple M1";
      return getParameter.call(this, parameter);
    };
    if (typeof WebGL2RenderingContext !== "undefined") {
      const getParameter2 = WebGL2RenderingContext.prototype.getParameter;
      WebGL2RenderingContext.prototype.getParameter = function (parameter) {
        if (parameter === 0x9245) return "Apple Inc.";
        if (parameter === 0x9246) return "Apple M1";
        return getParameter2.call(this, parameter);
      };
    }
  } catch {}
})();
