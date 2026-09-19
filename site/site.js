// Grimoire landing page behaviors: the ember backdrop, the sigil diagram,
// the brand diffusion cycle, and live release data. Static-host friendly —
// everything fails soft when the GitHub API is unreachable (offline,
// rate-limited, or the repos are still private).
(function () {
  "use strict";

  // --- Rising embers ------------------------------------------------------------
  // Fixed full-viewport canvas behind the page: three parallax depth layers of
  // warm motes drifting up with a gentle sway and a flicker, some tinted with
  // the theme accent. Density scales with the viewport; DPR capped at 2. Under
  // prefers-reduced-motion it draws one static frame; rAF pauses in background
  // tabs on its own.
  function initEmbers() {
    var canvas = document.querySelector("[data-embers]");
    if (!canvas) return;
    var ctx = canvas.getContext("2d");
    var reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    // layer: [embers per 10000 px², radius, rise px/frame]; depth 0..1 drives
    // parallax shift, rise speed, and brightness.
    var LAYERS = [[3.2, 1.1, 0.10], [1.9, 1.6, 0.22], [0.8, 2.3, 0.38]];
    var PARALLAX = 14, FLICKER = 0.55;
    var dpr = Math.min(devicePixelRatio || 1, 2);
    var embers = [], W = 0, H = 0, px = 0, py = 0, t = 0;
    var base = "244, 232, 210"; // warm ember white
    // Single theme: the accent is read once, at init.
    var accent = getComputedStyle(document.documentElement)
      .getPropertyValue("--mass-accent").trim() || "#d9a44a";

    function resize() {
      W = innerWidth; H = innerHeight;
      canvas.width = W * dpr; canvas.height = H * dpr;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      embers = [];
      LAYERS.forEach(function (l, li) {
        var depth = (li + 1) / LAYERS.length;
        var n = Math.round((W * H / 10000) * l[0]);
        for (var i = 0; i < n; i++) {
          embers.push({
            x: Math.random() * W, y: Math.random() * H,
            r: l[1] * (0.6 + Math.random() * 0.7), v: l[2], d: depth,
            amp: 4 + Math.random() * 14,        // sway width, px
            sw: 0.006 + Math.random() * 0.012,  // sway frequency
            ph: Math.random() * 6.28,
            // A flame's unsteadiness: most motes pulse in brightness.
            fl: Math.random() < FLICKER ? 0.06 + Math.random() * 0.16 : 0,
            accent: Math.random() < 0.2,
          });
        }
      });
      if (reduced) frame();
    }

    function frame() {
      t += 1;
      ctx.clearRect(0, 0, W, H);
      for (var i = 0; i < embers.length; i++) {
        var s = embers[i];
        s.y -= s.v * s.d; // rise; far layers rise slower
        if (s.y < -4) { s.y = H + 4; s.x = Math.random() * W; }
        var alpha = 0.2 + s.d * 0.6;
        if (s.fl) alpha *= 0.55 + 0.45 * Math.sin(t * s.fl + s.ph);
        if (s.accent) {
          ctx.globalAlpha = alpha; ctx.fillStyle = accent;
        } else {
          ctx.globalAlpha = 1;
          ctx.fillStyle = "rgba(" + base + "," + alpha.toFixed(3) + ")";
        }
        ctx.beginPath();
        // Sway is an offset at draw time, not a drift, so motes hold their lane.
        ctx.arc(s.x + Math.sin(t * s.sw + s.ph) * s.amp + px * PARALLAX * s.d,
                s.y + py * PARALLAX * s.d, s.r, 0, 6.28);
        ctx.fill();
      }
      ctx.globalAlpha = 1;
    }

    resize();
    addEventListener("resize", resize);
    if (!reduced) {
      addEventListener("pointermove", function (e) {
        px = e.clientX / W - 0.5;
        py = e.clientY / H - 0.5;
      });
      (function loop() { frame(); requestAnimationFrame(loop); })();
    }
  }

  // --- Sigil circle -------------------------------------------------------------
  // Builds the #how diagram: the daemon (glowing core inside three
  // counter-rotating arcs) on a drawn circle, linked to the vault's four
  // companions, with knowledge pulses riding the links on CSS motion paths
  // (styling + animation live in site.css).
  function initSigil() {
    var svg = document.querySelector("[data-sigil]");
    if (!svg) return;
    var NS = "http://www.w3.org/2000/svg";
    var HX = 360, HY = 150, RX = 185, RY = 88;
    // Nodes sit on the ellipse at the diagonals, so no spoke blocks the hub's
    // label. Each carries its role: who or what shares the circle.
    var NODES = [
      [229, 88, "you · the app", "end", -14],
      [491, 88, "vault · markdown", "start", 14],
      [491, 212, "agents · cli", "start", 14],
      [229, 212, "mass · embeddings", "end", -14],
    ];

    function el(name, attrs) {
      var e = document.createElementNS(NS, name);
      for (var k in attrs) e.setAttribute(k, attrs[k]);
      svg.appendChild(e);
      return e;
    }

    el("ellipse", { cx: HX, cy: HY, rx: RX, ry: RY, "class": "ring" });
    // Cardinal ticks: the circle's four extreme points, runic ornaments.
    [[HX, HY - RY], [HX - RX, HY], [HX, HY + RY], [HX + RX, HY]].forEach(function (p) {
      el("circle", { cx: p[0], cy: p[1], r: 1.6, "class": "tick" });
    });

    NODES.forEach(function (n, i) {
      var d = "M" + HX + " " + HY + " L" + n[0] + " " + n[1];
      el("path", { d: d, "class": "link" });
      var p = el("circle", { r: 2.4, "class": "pulse" });
      p.style.setProperty("--p", 'path("' + d + '")');
      p.style.setProperty("--d", (i * 0.9) + "s");
      el("circle", { cx: n[0], cy: n[1], r: 3, "class": "node" });
      el("circle", { cx: n[0], cy: n[1], r: 7, "class": "node-ring", "stroke-dasharray": "2 3" });
      var text = el("text", { x: n[0] + n[4], y: n[1] + 3 });
      text.setAttribute("text-anchor", n[3]);
      text.textContent = n[2];
    });

    el("circle", { cx: HX, cy: HY, r: 14, "class": "hub-glow" });
    el("circle", { cx: HX, cy: HY, r: 5, "class": "hub-core" });
    [el("path", { "class": "arc a1", "stroke-width": 1.6, d: arc(HX, HY, 12, -40, 140) }),
     el("path", { "class": "arc a2", "stroke-width": 1.2, d: arc(HX, HY, 19, 90, 240) }),
     el("path", { "class": "arc a3", "stroke-width": 1.0, d: arc(HX, HY, 26, -20, 60) })]
      .forEach(function (a) {
        a.style.setProperty("--cx", HX + "px");
        a.style.setProperty("--cy", HY + "px");
      });
    var hub = el("text", { x: HX, y: HY + 36 });
    hub.setAttribute("text-anchor", "middle");
    hub.textContent = "grimoire";

    function arc(cx, cy, r, a0, a1) {
      function pt(a) {
        var rad = (a - 90) * Math.PI / 180;
        return [cx + r * Math.cos(rad), cy + r * Math.sin(rad)];
      }
      var s = pt(a0), e = pt(a1), large = a1 - a0 > 180 ? 1 : 0;
      return "M" + s[0] + " " + s[1] + " A" + r + " " + r + " 0 " + large + " 1 " + e[0] + " " + e[1];
    }
  }

  // --- Brand diffusion cycle ------------------------------------------------------
  // The hero mark rolls GRIMOIRE → runes → 127.0.0.1 with a
  // per-character diffusion: during a transition every character churns
  // through the target variant's glyph pool and resolves left-to-right.
  // Between cycles it throws brief glitch flickers. Static under
  // prefers-reduced-motion. The daemon binds an ephemeral loopback port
  // (published to daemon.port), so the address — not a port number — is the
  // constant on the wire.
  function initBrandCycle() {
    var el = document.querySelector("[data-brand-cycle]");
    if (!el) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

    // Each variant scrambles in its own alphabet: letters condense into
    // GRIMOIRE, Elder Futhark runes into the mark, digits into the loopback.
    var VARIANTS = [
      { text: "GRIMOIRE", pool: "ABCDEFGHIJKLMNOPQRSTUVWXYZ" },
      { text: "ᚷᚱᛁᛗᛟᛁᚱᛖ", pool: "ᚠᚢᚦᚨᚱᚲᚷᚹᚺᚾᛁᛃᛇᛈᛉᛊᛏᛒᛖᛗᛚᛜᛞᛟ" },
      { text: "127.0.0.1", pool: "0123456789." },
    ];
    var TICK = 45;        // ms per scramble frame
    var RESOLVE = 220;    // ms between successive characters locking in
    var DWELL = 3600;     // ms a variant stays before diffusing to the next
    var idx = 0;

    function glyph(pool) {
      return pool[(Math.random() * pool.length) | 0];
    }

    function scrambled(pool, n) {
      var s = "";
      for (var i = 0; i < n; i++) s += glyph(pool);
      return s;
    }

    // Two phases so different-length variants never snap: first the churning
    // mark grows/shrinks one cell at a time to the target length, then the
    // cells resolve left-to-right into the target.
    function diffuseTo(next) {
      var len = el.textContent.length;
      var tick = 0;
      var morph = setInterval(function () {
        if (len === next.text.length) { clearInterval(morph); resolve(); return; }
        if (++tick % 2 === 0) len += len < next.text.length ? 1 : -1;
        el.textContent = scrambled(next.pool, len);
      }, TICK);
      function resolve() {
        var start = Date.now();
        var timer = setInterval(function () {
          var t = Date.now() - start, out = "", done = true;
          for (var i = 0; i < next.text.length; i++) {
            if (t > i * RESOLVE + RESOLVE) {
              out += next.text[i];
            } else {
              done = false;
              out += glyph(next.pool);
            }
          }
          el.textContent = out;
          if (done) { el.textContent = next.text; clearInterval(timer); }
        }, TICK);
      }
    }

    setInterval(function () {
      idx = (idx + 1) % VARIANTS.length;
      diffuseTo(VARIANTS[idx]);
    }, DWELL);

    (function flicker() {
      setTimeout(function () {
        el.classList.add("flicker");
        setTimeout(function () { el.classList.remove("flicker"); flicker(); }, 60 + Math.random() * 140);
      }, 1200 + Math.random() * 2600);
    })();
  }

  // --- Live release data ------------------------------------------------------
  // Show the current version on the hero button, and light up any download that
  // exists in the latest release ("coming soon" rows flip to links the moment
  // the asset is uploaded — no site change needed).
  function wireReleases() {
    var repos = {};
    document.querySelectorAll("[data-asset], [data-asset-cell]").forEach(function (el) {
      var key = el.getAttribute("data-asset") || el.getAttribute("data-asset-cell");
      repos[key.split("/")[0]] = true;
    });
    Object.keys(repos).forEach(function (repo) {
      fetch("https://api.github.com/repos/chinese-room-solutions/" + repo + "/releases/latest", {
        headers: { Accept: "application/vnd.github+json" },
      })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (rel) {
          if (!rel) return;
          var have = {};
          (rel.assets || []).forEach(function (a) { have[a.name] = true; });
          if (repo === "grimoire") {
            document.querySelectorAll("[data-version]").forEach(function (el) {
              el.textContent = rel.tag_name.replace(/^v/, "");
            });
          }
          // Links whose asset is missing from the latest release degrade to
          // "coming soon"; placeholder cells with a present asset become links.
          document.querySelectorAll('[data-asset^="' + repo + '/"]').forEach(function (a) {
            if (!have[a.getAttribute("data-asset").split("/")[1]]) {
              var s = document.createElement("span");
              s.className = "soon";
              s.textContent = "coming soon";
              a.replaceWith(s);
            }
          });
          document.querySelectorAll('[data-asset-cell^="' + repo + '/"]').forEach(function (td) {
            var name = td.getAttribute("data-asset-cell").split("/")[1];
            if (have[name]) {
              var a = document.createElement("a");
              a.href = td.getAttribute("data-href");
              a.textContent = name;
              td.classList.remove("soon");
              td.replaceChildren(a);
            }
          });
        })
        .catch(function () { /* offline / rate-limited / private — keep defaults */ });
    });
  }

  // --- Click-to-copy quickstart commands ---------------------------------------

  // popCopied floats a "copied" label up from the pointer and lets it vaporize.
  // The inline tick can be scrolled out of a long command's view; this can't.
  // Anchored to the element's left edge when the click carries no coordinates
  // (keyboard activation reports 0,0).
  function popCopied(ev, el) {
    var x = ev.clientX, y = ev.clientY;
    if (!x && !y) {
      var r = el.getBoundingClientRect();
      x = r.left + Math.min(r.width, 90) / 2;
      y = r.top;
    }
    var pop = document.createElement("span");
    pop.className = "copy-pop";
    pop.textContent = "copied";
    pop.style.left = x + "px";
    pop.style.top = y - 14 + "px";
    document.body.appendChild(pop);
    pop.addEventListener("animationend", function () { pop.remove(); });
  }

  function initCopy() {
    document.querySelectorAll("pre .cmd").forEach(function (el) {
      el.title = "Click to copy";
      el.addEventListener("click", function (ev) {
        var text = el.textContent;
        // Race a short timeout: writeText can hang without ever settling when
        // the browser withholds clipboard permission.
        var write = navigator.clipboard
          ? Promise.race([
              navigator.clipboard.writeText(text),
              new Promise(function (_, reject) { setTimeout(reject, 250); }),
            ])
          : Promise.reject();
        write
          .catch(function () {
            // http:// preview or denied permission — legacy fallback.
            var ta = document.createElement("textarea");
            ta.value = text;
            document.body.appendChild(ta);
            ta.select();
            document.execCommand("copy");
            ta.remove();
          })
          .then(function () {
            el.classList.add("copied");
            setTimeout(function () { el.classList.remove("copied"); }, 1300);
            popCopied(ev, el);
          });
      });
    });
  }

  function initPage() { initEmbers(); initSigil(); initBrandCycle(); wireReleases(); initCopy(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initPage);
  } else {
    initPage();
  }
})();
