// Grimoire landing page behaviors: the constellation backdrop, the SDK-style
// theme picker, the brand flicker, and live release data. Static-host
// friendly — everything fails soft when the GitHub API is unreachable
// (offline, rate-limited, or the repos are still private).
(function () {
  "use strict";

  // --- Knowledge-graph constellation --------------------------------------------
  // Fixed full-viewport canvas behind the page: a faint graph of drifting
  // notes — nodes wander slowly, each linking to a few near neighbors (its
  // degree is fixed at birth, so the weave mixes chains, triangles, and
  // stars), and pulses walk multi-hop paths across the links. Density scales
  // with the viewport; DPR capped at 2. Under prefers-reduced-motion it draws
  // one static frame; rAF pauses in background tabs on its own.
  function initGraph() {
    var canvas = document.querySelector("[data-graph]");
    if (!canvas) return;
    var ctx = canvas.getContext("2d");
    var reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    // node per px² of viewport, capped so huge screens stay cheap. LINK and
    // the light-base density live in readColors — Cream runs a denser weave.
    var NODE_PER_PX = 1 / 20000, MAX_NODES = 120;
    var DRIFT = 0.06;      // max node speed, px/frame
    var PARALLAX = 12;
    var TOPO_TICKS = 20;   // frames between graph rebuilds (drift is slow)
    var dpr = Math.min(devicePixelRatio || 1, 2);
    var nodes = [], edges = [], adj = [];
    var W = 0, H = 0, px = 0, py = 0, tick = 0;
    // Theme colors: read at init and re-read on the mass-theme event. On a
    // light base the dark-tuned weave drowns in the pale background — too
    // sparse to read and too faint where it exists — so Cream runs a denser
    // graph with a longer link reach, drawn in the text ink at full-strength
    // alphas. Dark keeps its subtle accent weave.
    var accent, ink, gain, light, LINK;
    function readColors() {
      light = document.documentElement.classList.contains("sl-theme-light");
      var style = getComputedStyle(document.documentElement);
      accent = style.getPropertyValue("--mass-accent").trim() || "#d9a44a";
      ink = style.getPropertyValue(light ? "--mass-text" : "--mass-text-muted").trim() || "#999";
      gain = light ? 1.5 : 1;
      LINK = light ? 230 : 150;
    }
    readColors();
    // applyTheme dispatches on document (no bubbles), so listen there — a
    // window listener never sees it.
    document.addEventListener("mass-theme", function () {
      readColors();
      resize(); // re-roll the topology: Cream's density and link reach differ
      if (reduced) frame(); // the static frame needs a repaint in the new colors
    });
    // Active pulses: {path: [node idx...], t0, dur} — a walk across linked nodes.
    var pulses = [], nextPulse = 0;

    function resize() {
      W = innerWidth; H = innerHeight;
      canvas.width = W * dpr; canvas.height = H * dpr;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      var n = Math.min(Math.round(W * H * (light ? 1 / 12000 : NODE_PER_PX)), MAX_NODES);
      nodes = [];
      for (var i = 0; i < n; i++) {
        var a = Math.random() * 6.28;
        nodes.push({
          x: Math.random() * W, y: Math.random() * H,
          vx: Math.cos(a) * DRIFT * Math.random(), vy: Math.sin(a) * DRIFT * Math.random(),
          d: 0.35 + Math.random() * 0.65,       // depth: parallax + presence
          r: 1 + Math.random() * 1.2,
          cap: 2 + (Math.random() * 3 | 0),     // links this node may hold: 2..4
        });
      }
      edges = []; adj = [];
      pulses = [];
      rebuild();
      if (reduced) frame();
    }

    // Rebuild the topology: each node links to its nearest neighbors that
    // still have degree headroom. Fixed caps seed variety — chains, loops,
    // triangles, and the occasional star — instead of one uniform mesh.
    function rebuild() {
      edges = []; adj = nodes.map(function () { return []; });
      var linked = {};
      for (var i = 0; i < nodes.length; i++) {
        var cand = [];
        for (var j = 0; j < nodes.length; j++) {
          if (j === i) continue;
          var dx = nodes[i].x - nodes[j].x, dy = nodes[i].y - nodes[j].y;
          var d2 = dx * dx + dy * dy;
          if (d2 < LINK * LINK) cand.push([d2, j]);
        }
        cand.sort(function (a, b) { return a[0] - b[0]; });
        for (var k = 0; k < cand.length && adj[i].length < nodes[i].cap; k++) {
          j = cand[k][1];
          if (adj[j].length >= nodes[j].cap || linked[i * nodes.length + j]) continue;
          linked[i * nodes.length + j] = linked[j * nodes.length + i] = true;
          adj[i].push(j); adj[j].push(i);
          edges.push([i, j]);
        }
      }
    }

    // Wander: nodes keep their speed but get a new heading now and then, so
    // the graph keeps re-forming instead of settling.
    function wander(s) {
      if (Math.random() < 0.004) {
        var a = Math.random() * 6.28, v = DRIFT * (0.3 + Math.random() * 0.7);
        s.vx = Math.cos(a) * v; s.vy = Math.sin(a) * v;
      }
      s.x += s.vx; s.y += s.vy;
      if (s.x < -8) s.x = W + 8; else if (s.x > W + 8) s.x = -8;
      if (s.y < -8) s.y = H + 8; else if (s.y > H + 8) s.y = -8;
    }

    // A pulse walks 2..4 linked hops from a random edge, never doubling back.
    function spawnPulse() {
      if (!edges.length) return;
      var e = edges[(Math.random() * edges.length) | 0];
      var path = [Math.random() < 0.5 ? e[0] : e[1]];
      path.push(path[0] === e[0] ? e[1] : e[0]);
      while (path.length < 2 + (Math.random() * 3 | 0)) {
        var from = adj[path[path.length - 1]];
        var next = from.filter(function (n) { return n !== path[path.length - 2]; });
        if (!next.length) break;
        path.push(next[(Math.random() * next.length) | 0]);
      }
      if (path.length < 2) return;
      pulses.push({ path: path, t0: Date.now(), dur: 500 * (path.length - 1) + Math.random() * 500 });
    }

    function frame() {
      ctx.clearRect(0, 0, W, H);
      var i, s;
      for (i = 0; i < nodes.length; i++) wander(nodes[i]);
      if (++tick % TOPO_TICKS === 0) rebuild();

      // Edges: alpha fades with distance, so links surface and dissolve as
      // the nodes they bind drift. On the light base the accent drowns in
      // the pale background, so the weave draws in the text ink (espresso)
      // at full-strength alphas; dark keeps its subtle accent weave.
      ctx.strokeStyle = light ? ink : accent; ctx.lineWidth = 1;
      for (i = 0; i < edges.length; i++) {
        var A = nodes[edges[i][0]], B = nodes[edges[i][1]];
        var dx = A.x - B.x, dy = A.y - B.y;
        var fade = 1 - Math.sqrt(dx * dx + dy * dy) / LINK;
        if (fade <= 0) continue;
        ctx.globalAlpha = light
          ? Math.min(1, fade * 0.38)
          : 0.14 * fade * Math.min(A.d, B.d);
        ctx.beginPath();
        ctx.moveTo(A.x + px * PARALLAX * A.d, A.y + py * PARALLAX * A.d);
        ctx.lineTo(B.x + px * PARALLAX * B.d, B.y + py * PARALLAX * B.d);
        ctx.stroke();
      }

      // Pulses walk their paths segment by segment.
      if (pulses.length < 4 && Date.now() > nextPulse) {
        spawnPulse();
        nextPulse = Date.now() + 450 + Math.random() * 800;
      }
      for (i = pulses.length - 1; i >= 0; i--) {
        var p = pulses[i];
        var t = (Date.now() - p.t0) / p.dur;
        if (t >= 1) { pulses.splice(i, 1); continue; }
        var seg = Math.min(t * (p.path.length - 1), p.path.length - 2) | 0;
        var lt = t * (p.path.length - 1) - seg;
        var P = nodes[p.path[seg]], Q = nodes[p.path[seg + 1]];
        var ex = P.x + (Q.x - P.x) * lt, ey = P.y + (Q.y - P.y) * lt;
        var glow = Math.sin(t * Math.PI); // ease: bright mid-travel
        var depth = Math.min(P.d, Q.d);
        ctx.globalAlpha = 0.85 * glow;
        ctx.fillStyle = accent;
        ctx.beginPath(); ctx.arc(ex + px * PARALLAX * depth, ey + py * PARALLAX * depth, 1.6, 0, 6.28); ctx.fill();
        ctx.globalAlpha = 0.25 * glow;
        ctx.beginPath(); ctx.arc(ex + px * PARALLAX * depth, ey + py * PARALLAX * depth, 4, 0, 6.28); ctx.fill();
      }

      // Nodes on top.
      ctx.fillStyle = ink;
      for (i = 0; i < nodes.length; i++) {
        s = nodes[i];
        ctx.globalAlpha = Math.min(1, (0.25 + s.d * 0.5) * gain);
        ctx.beginPath();
        ctx.arc(s.x + px * PARALLAX * s.d, s.y + py * PARALLAX * s.d, s.r, 0, 6.28);
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

  // --- Themes -------------------------------------------------------------------
  // Mirrors mass-sdk/uikit/theme.js (and MASS's own landing page): a theme is
  // the Shoelace base class (sl-theme-dark|light) plus an overlay class for
  // pluggable themes — synthwave layers on dark. applyTheme restamps <html>;
  // the mass-theme event lets canvas painters re-read the theme's colors.
  var THEMES = {
    dark: {},
    light: { base: "light" },
    synthwave: { overlay: "sl-theme-synthwave" },
  };

  function applyTheme(name) {
    // Unknown or retired name falls back to dark, and is normalized so it does
    // not get written back to storage.
    if (!THEMES[name]) name = "dark";
    var info = THEMES[name];
    var h = document.documentElement;
    Array.prototype.slice.call(h.classList).forEach(function (c) {
      if (c.indexOf("sl-theme-") === 0) h.classList.remove(c);
    });
    h.classList.add(info.base === "light" ? "sl-theme-light" : "sl-theme-dark");
    if (info.overlay) h.classList.add(info.overlay);
    // The active theme's exact name — the same contract the SDK's Layout and
    // massSetTheme maintain (theme.css keys its Carbon block on it).
    h.setAttribute("data-theme", name);
    document.dispatchEvent(new CustomEvent("mass-theme"));
    document.querySelectorAll("[data-theme-pick]").forEach(function (b) {
      b.setAttribute("aria-pressed", String(b.getAttribute("data-theme-pick") === name));
    });
    try { localStorage.setItem("grimoire-site-theme", name); } catch (e) { /* private mode */ }
  }

  function initTheme() {
    var fromQuery = new URLSearchParams(location.search).get("theme");
    var saved = null;
    try { saved = localStorage.getItem("grimoire-site-theme"); } catch (e) { /* private mode */ }
    applyTheme(fromQuery || saved || "dark");
    document.querySelectorAll("[data-theme-pick]").forEach(function (b) {
      b.addEventListener("click", function () { applyTheme(b.getAttribute("data-theme-pick")); });
    });
  }

  // --- Brand flicker ------------------------------------------------------------
  // The hero mark stays GRIMOIRE and throws rare glitch flickers between long
  // quiet stretches. Static under prefers-reduced-motion.
  function initBrandFlicker() {
    var el = document.querySelector("[data-brand-flicker]");
    if (!el) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

    (function flicker() {
      setTimeout(function () {
        el.classList.add("flicker");
        setTimeout(function () { el.classList.remove("flicker"); flicker(); }, 60 + Math.random() * 140);
      }, 1200 + Math.random() * 2600);
    })();
  }

  // --- Live release data ------------------------------------------------------
  // Show the current version on the hero button, and degrade any download
  // link whose asset is missing from the latest release to "coming soon"
  // (no site change needed).
  function wireReleases() {
    var repos = {};
    document.querySelectorAll("[data-asset]").forEach(function (el) {
      repos[el.getAttribute("data-asset").split("/")[0]] = true;
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
          document.querySelectorAll('[data-asset^="' + repo + '/"]').forEach(function (a) {
            if (!have[a.getAttribute("data-asset").split("/")[1]]) {
              var s = document.createElement("span");
              s.className = "soon";
              s.textContent = "coming soon";
              a.replaceWith(s);
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

  function initPage() { initGraph(); initTheme(); initBrandFlicker(); wireReleases(); initCopy(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initPage);
  } else {
    initPage();
  }
})();
