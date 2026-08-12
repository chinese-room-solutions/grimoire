(function () {
  "use strict";

  var getEl = function (id) { return document.getElementById(id); };

  // One daemon serves every vault, so every request has to say which one it is
  // for. Datastar actions carry the gVault signal automatically; a plain fetch()
  // doesn't, so it goes through apiURL, which appends the page's vault (read from
  // the gVault signal input the page seeds).
  function pageVault() {
    var el = document.querySelector('[data-bind="gVault"]');
    return el ? el.value || "" : "";
  }

  function apiURL(path) {
    var vault = pageVault();
    if (!vault) return path;
    return path + (path.indexOf("?") === -1 ? "?" : "&") + "vault=" + encodeURIComponent(vault);
  }

  // matchesFilter reports whether haystack contains every whitespace-separated
  // word of query, in any order and case-insensitively — so "devops kernel"
  // matches "DevOps QA - Kernel". An empty query matches everything. Shared by the
  // sessions, files, and graph title filters.
  function matchesFilter(haystack, query) {
    var q = (query || "").trim().toLowerCase();
    if (!q) return true;
    var hay = (haystack || "").toLowerCase();
    var words = q.split(/\s+/);
    for (var i = 0; i < words.length; i++) {
      if (hay.indexOf(words[i]) === -1) return false;
    }
    return true;
  }

  // The model dropdowns, session list, and file tree populate themselves via
  // data-init in the templ (Datastar fires it when each element loads), so no JS
  // bootstrap/readiness-poll is needed here.

  var setSignal = function (name, value) {
    var el = document.querySelector('[data-bind="' + name + '"]');
    if (el) {
      el.value = typeof value === "string" ? value : JSON.stringify(value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    }
  };

  // Set a signal, then fire the @post trigger on the next frame so Datastar has
  // committed the signal into its store before the request serializes it.
  // Without the defer, a synchronous click can post the previous value.
  function fireWithSignal(name, value, triggerId) {
    setSignal(name, value);
    requestAnimationFrame(function () {
      var t = getEl(triggerId);
      if (t) t.click();
    });
  }

  // Adding a vault happens in-process: the daemon opens the chosen folder (it
  // keeps every vault it serves resident) and reports {reload:true} when it isn't
  // the one this page is showing, so we reload onto it. With no path in the body
  // the daemon raises the native folder dialog; a client with no window to raise
  // it in gets {needsPath:true} back instead, and onNeedsPath (the Vaults tab's
  // inline field) takes over. The model selects, concurrency, and Reindex are
  // wired in the templ.
  function openVault(body, btn, onNeedsPath) {
    if (btn) btn.loading = true;
    fetch(apiURL("api/vaults/add"), { method: "POST", body: body })
      .then(function (r) { return r.json(); })
      .then(function (res) {
        // Navigate to the vault by name rather than reloading: this page's URL
        // may pin a different ?vault=, and a reload would land back on it.
        if (res && res.reload) { location.assign(vaultURL(res.vault)); return; }
        if (res && res.needsPath && onNeedsPath) { onNeedsPath(); return; }
        // ok:false (picker cancelled) or ok:true with no change: nothing to do.
      })
      .catch(function () { /* open failed; leave the UI as-is */ })
      .finally(function () { if (btn) btn.loading = false; });
  }

  // pathBody wraps an absolute vault path as the form body api/vaults/add takes.
  function pathBody(path) {
    var fd = new FormData();
    fd.append("path", path);
    return fd;
  }

  // vaultURL is the page for one vault. Opening a vault is a navigation, not a
  // reload: the workspace, file tree and session history all come from the
  // vault's own state, which the page restores on load.
  function vaultURL(path) {
    return path ? "?vault=" + encodeURIComponent(path) : location.pathname;
  }

  function initVault() {
    // The vault button is the trigger of an sl-dropdown (the gear-style Vault
    // menu), so Shoelace opens it on click — no manual show() needed. The menu
    // holds this vault's own settings; choosing a different vault is the Vaults
    // tab in the sidebar.

    // Empty state: pick a new folder, or open a known vault by its path.
    var emptyOpen = getEl("g-vault-empty-open");
    if (emptyOpen) emptyOpen.addEventListener("click", function () { openVault(null, emptyOpen); });
    var empty = getEl("g-vault-empty");
    if (empty) empty.addEventListener("click", function (e) {
      var row = e.target.closest(".g-vault-recent");
      if (!row) return;
      openVault(pathBody(row.getAttribute("data-vault-path") || ""), row);
    });
  }

  // The Vaults sidebar tab: the daemon's whole vault list, with the one this page
  // is showing highlighted. Switching is a reload onto ?vault=, not an in-place
  // swap — every open tab, the file tree and the session history belong to the
  // vault, and the page restores all of it from that vault's own UI state on load.
  var vaults = (function () {
    // Re-render by clicking the templ's data-init trigger's twin: the list is a
    // server-rendered fragment, so a change is a re-fetch, never a DOM edit here.
    function refresh() {
      var t = getEl("g-vaults-render-trigger");
      if (t) t.click();
    }

    // Reveal the absolute-path field (a browser tab has no native folder dialog)
    // and focus it. Enter submits; the field stays open so a typo can be fixed.
    function revealPathField() {
      var wrap = getEl("g-vault-add-path");
      var input = getEl("g-vault-add-input");
      if (!wrap || !input) return;
      wrap.classList.add("g-vault-add-path-open");
      if (typeof input.focus === "function") input.focus();
    }

    function forget(path, name) {
      confirmForget(name, path, function () {
        fetch(apiURL("api/vaults/forget"), { method: "POST", body: pathBody(path) })
          .then(function () { refresh(); })
          .catch(function () { /* the list is unchanged; nothing to undo. */ });
      });
    }

    function init() {
      var addBtn = getEl("g-vault-add");
      if (addBtn) addBtn.addEventListener("click", function () {
        openVault(null, addBtn, revealPathField);
      });
      var input = getEl("g-vault-add-input");
      if (input) input.addEventListener("keydown", function (e) {
        if (e.key !== "Enter") return;
        var path = (input.value || "").trim();
        if (path) openVault(pathBody(path), null);
      });

      var list = getEl("g-vaults");
      if (!list) return;
      // One delegated click for the whole list: rows are replaced wholesale on
      // every re-render, so per-row listeners would have to be re-attached.
      list.addEventListener("click", function (e) {
        var row = e.target.closest(".g-vault-row");
        if (!row) return;
        var path = row.getAttribute("data-vault-path") || "";
        if (e.target.closest(".g-vault-forget")) {
          forget(path, row.querySelector(".g-vault-name").textContent);
          return;
        }
        // The overflow menu is not a row click.
        if (e.target.closest(".g-vault-row-menu")) return;
        if (row.getAttribute("data-vault-available") !== "true") return;
        if (row.classList.contains("g-vault-row-current")) return;
        location.assign(vaultURL(path));
      });
    }
    return { init: init };
  })();

  // confirmForget asks before dropping a vault from the list. Its own dialog, not
  // the delete one: forgetting removes nothing from disk, and the copy has to say
  // so or it reads as a delete.
  function confirmForget(name, path, onConfirm) {
    var dialog = getEl("g-forget-dialog");
    var body = getEl("g-forget-body");
    if (!dialog) return;
    if (body) {
      body.textContent = "Remove “" + name + "” from the vault list? The folder " +
        "and its notes stay on disk at " + path + " — add it again any time.";
    }
    var confirm = getEl("g-forget-confirm");
    var cancel = getEl("g-forget-cancel");
    // Fresh handlers per ask, so a previous vault's path can't be forgotten by a
    // later confirmation.
    function close() {
      dialog.hide();
      if (confirm) confirm.onclick = null;
      if (cancel) cancel.onclick = null;
    }
    if (confirm) confirm.onclick = function () { close(); onConfirm(); };
    if (cancel) cancel.onclick = close;
    dialog.show();
  }

  // Hover calm-down for the scrollable lists: while content wheel-scrolls under a
  // stationary pointer the engine re-resolves :hover per frame, so the row
  // semi-selection hops between rows and the scrollbar's hover state churns with
  // it (the thumb visibly blinks). Suspending hit-testing on the rows
  // (pointer-events:none via g-scroll-calm) while scroll events stream — restored
  // shortly after the last one — keeps hover still for the whole ride. Skipped
  // mid-drag: drag auto-scroll needs live hit-testing for its drop targets.
  function calmHoverWhileScrolling(id) {
    var el = getEl(id);
    if (!el) return;
    var timer = null;
    el.addEventListener("scroll", function () {
      if (el.querySelector(".g-dragging")) return;
      el.classList.add("g-scroll-calm");
      if (timer) clearTimeout(timer);
      timer = setTimeout(function () { el.classList.remove("g-scroll-calm"); }, 140);
    }, { passive: true });
  }

  // Theme picker: a palette dropdown (in the bottom bar) listing every registered
  // theme (Carbon/Cream built-ins + any pluggable ones). Selecting one applies it
  // live via the SDK's window.massSetTheme, persists it through the api/settings
  // endpoint, and syncs the menu's checkmarks. This is Grimoire's only theme
  // control — the gear dropdown's Theme select is hidden.
  // The palette dropdown's last row isn't a theme: it opens the Extensions
  // dialog on the Themes tab. Kept in sync with ui.ThemeBrowseValue.
  var THEME_BROWSE = "__browse";

  var themePicker = (function () {
    function menu() {
      var picker = getEl("g-theme-picker");
      return picker ? picker.querySelector("sl-menu") : null;
    }
    function rows() {
      var m = menu();
      return m ? Array.prototype.slice.call(m.querySelectorAll('sl-menu-item[type="checkbox"]')) : [];
    }

    // Apply a theme live, persist it, and sync the menu's single checkmark. The
    // one path every theme switch goes through — the picker and an Extensions
    // install alike.
    function apply(name) {
      if (!name) return;
      // $gTheme drives the theme-reactive markup (the Extensions dialog's
      // active check data-shows against it).
      setSignal("gTheme", name);
      if (window.massSetTheme) window.massSetTheme(name);
      // Keep a single item checked (checkbox items toggle independently otherwise).
      rows().forEach(function (item) { item.checked = item.value === name; });
      // Notify modules that cache theme colours (e.g. the canvas graph) so they
      // re-read the new palette — the CSS-var switch alone doesn't reach a canvas.
      document.dispatchEvent(new CustomEvent("grimoire:theme", { detail: { theme: name } }));
      fetch("api/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ appTheme: name }),
      }).catch(function () { /* persistence is best-effort. */ });
    }

    // Add a freshly installed theme to the dropdown without a page reload, so
    // the picker matches the registry the moment the install lands. The row goes
    // before the divider that separates the themes from the "Browse themes…" row.
    function add(name, label) {
      var m = menu();
      if (!m || !name) return;
      if (rows().some(function (item) { return item.value === name; })) return;
      var item = document.createElement("sl-menu-item");
      item.setAttribute("type", "checkbox");
      // setAttribute, not .value: Shoelace doesn't reflect the property back to
      // the attribute, and the row must match the same selectors as a
      // server-rendered one before the custom element upgrades.
      item.setAttribute("value", name);
      item.textContent = label || name;
      var divider = m.querySelector("sl-divider");
      if (divider) m.insertBefore(item, divider);
      else m.appendChild(item);
    }

    // Drop a removed theme's row. Removing the theme in use leaves the page on a
    // stylesheet that no longer exists, so fall back to the first remaining one.
    function remove(name) {
      var all = rows();
      var gone = null;
      all.forEach(function (item) { if (item.value === name) gone = item; });
      if (!gone) return;
      var wasActive = gone.checked;
      gone.remove();
      if (wasActive) {
        var next = rows()[0];
        if (next) apply(next.value);
      }
    }

    function init() {
      var m = menu();
      if (!m) return;
      m.addEventListener("sl-select", function (evt) {
        var next = evt.detail.item.value;
        if (!next) return;
        if (next === THEME_BROWSE) { extensions.open("ext-themes"); return; }
        apply(next);
      });
    }

    return { init: init, apply: apply, add: add, remove: remove };
  })();

  // Extensions dialog: one overlay browsing and managing the installable extras
  // — themes and kernels — reached from the bottom bar's puzzle icon or the
  // palette dropdown's "Browse themes…" row. Each tab's rows are server-rendered
  // fragments; the single search box filters the visible tab client-side, and
  // each section shows one window of matching rows at a time behind its own
  // "Show More" row.
  var extensions = (function () {
    // Each tab's list-render trigger, by panel name. A tab is fetched when it is
    // first shown and re-fetched after every install or remove.
    var TRIGGERS = {
      "ext-themes": "g-ext-themes-trigger",
      "ext-kernels": "g-ext-kernels-trigger",
    };

    // Rows one "Show More" click adds, and the current window per section (keyed
    // panel id + section index, so a re-render keeps each section where it was).
    var EXT_PAGE = 5;
    var windows = {};

    function eachSection(fn) {
      document.querySelectorAll("#g-extensions-dialog .g-ext-panel").forEach(function (panel) {
        panel.querySelectorAll(".g-ext-section").forEach(function (section, i) {
          fn(section, panel.id + ":" + i);
        });
      });
    }

    // Show the rows matching the filter up to each section's window, hide the
    // rest, and trail a "Show More" row while any are held back. Both tabs are
    // walked: a hidden panel's rows cost nothing to touch and stay correct when
    // the tab is shown.
    //
    // Runs from a MutationObserver on the panels, so it must be idempotent in
    // its DOM writes — it only adds or removes the "Show More" row when that
    // row's presence actually has to change, and a no-op pass queues no further
    // mutations.
    function applyFilter() {
      var input = getEl("g-ext-filter");
      var q = input ? input.value : "";
      eachSection(function (section, key) {
        var limit = windows[key] || EXT_PAGE;
        var shown = 0;
        var held = 0;
        section.querySelectorAll(".g-ext-row").forEach(function (row) {
          var match = matchesFilter(row.getAttribute("data-g-ext-filter") || "", q);
          if (match && shown < limit) {
            shown++;
            row.hidden = false;
            return;
          }
          if (match) held++;
          row.hidden = true;
        });
        showMoreRow(section, key, held > 0);
      });
    }

    // Add or drop a section's "Show More" row. The row is client-side because
    // the window is: the server sends every row and this decides what fits.
    function showMoreRow(section, key, wanted) {
      var row = section.querySelector(".g-ext-more");
      if (!wanted) {
        if (row) row.remove();
        return;
      }
      if (row) return;
      var icon = document.createElement("sl-icon");
      icon.setAttribute("slot", "prefix");
      icon.setAttribute("name", "chevron-down");
      var btn = document.createElement("sl-button");
      btn.setAttribute("size", "small");
      btn.setAttribute("variant", "text");
      btn.appendChild(icon);
      btn.appendChild(document.createTextNode("Show More"));
      btn.addEventListener("click", function () {
        windows[key] = (windows[key] || EXT_PAGE) + EXT_PAGE;
        applyFilter();
      });
      row = document.createElement("div");
      row.className = "g-ext-more";
      row.appendChild(btn);
      section.appendChild(row);
    }

    // Rewind every section to its first window. A fresh search (or a fresh open)
    // starts at the top rather than inside someone else's paging.
    function resetWindows() {
      windows = {};
    }

    // Fetch a tab's list. Datastar owns the request (the trigger is a hidden
    // @get button), so the response patches the panel with no JSON to parse.
    function load(panel) {
      var trigger = getEl(TRIGGERS[panel]);
      if (trigger) trigger.click();
    }

    function activePanel() {
      var tabs = getEl("g-ext-tabs");
      var active = tabs ? tabs.querySelector("sl-tab[active]") : null;
      return active ? active.getAttribute("panel") : "ext-themes";
    }

    function open(panel) {
      var dialog = getEl("g-extensions-dialog");
      if (!dialog) return;
      var tabs = getEl("g-ext-tabs");
      if (tabs && panel && typeof tabs.show === "function") tabs.show(panel);
      resetWindows();
      dialog.show();
      load(panel || activePanel());
    }

    // Call the JSON API a row's button addresses (the same endpoints the CLI
    // uses) and re-render that tab. Returns the decoded body, or null on
    // failure — a failure always toasts, so the button never dead-ends silently
    // (an unpublished registry artifact, say, 503s here).
    function call(kind, action, body) {
      return fetch(apiURL("api/v1/" + kind + "/" + action), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      }).then(function (r) {
        if (r.ok) return r.json();
        return r.text().then(function (t) {
          fail(kind, action, window.massErrorText(t) || "HTTP " + r.status);
          return null;
        });
      }, function () {
        fail(kind, action, "the app isn't responding");
        return null;
      });
    }

    function fail(kind, action, reason) {
      window.massToast("Couldn't " + action + " " + kind + ": " + reason + ".",
        { variant: "danger" });
    }

    // Install: a theme joins the palette dropdown but is NOT applied — the user
    // activates it explicitly (row click or palette) when they want it.
    function install(btn) {
      var kind = btn.getAttribute("data-g-kind");
      var id = btn.getAttribute("data-g-id");
      btn.loading = true;
      call(kind, "install", { name: btn.getAttribute("data-g-pkg") }).then(function (res) {
        btn.loading = false;
        if (!res) return;
        if (kind === "theme") {
          themePicker.add(id, res.label || id);
        } else {
          // The backend resolves the new kernel immediately; re-render the open
          // note so its blocks pick up their Run buttons.
          offers = null;
          refreshPreview();
        }
        load(activePanel());
      });
    }

    // ── Point-of-use kernel install ──────────────────────────────────
    // A code block whose language nothing can run is a dead end. When the
    // registry offers a kernel for that language, the block gets an inline
    // install button instead: install, re-render the note, run the block.

    var offers = null; // family → {pkg, version}, fetched once per session.

    function kernelOffers() {
      if (offers) return offers;
      offers = fetch(apiURL("api/v1/kernel/list"))
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (res) {
          var byFamily = {};
          ((res && res.available) || []).forEach(function (p) {
            if (!p.installed) byFamily[(p.family || "").toLowerCase()] = p;
          });
          return byFamily;
        })
        .catch(function () { return {}; }); // offline: no offers, message stands.
      return offers;
    }

    // Fill every empty install slot the open note left behind. Slots whose
    // language the registry can't serve stay empty (and hidden by CSS).
    function fillInstallSlots() {
      var slots = document.querySelectorAll(".g-code-install:empty");
      if (!slots.length) return;
      kernelOffers().then(function (byFamily) {
        slots.forEach(function (slot) {
          var pkg = byFamily[(slot.getAttribute("data-g-lang") || "").toLowerCase()];
          if (!pkg || slot.firstChild) return;
          var btn = document.createElement("sl-button");
          btn.className = "g-code-install-btn";
          btn.setAttribute("size", "small");
          btn.setAttribute("variant", "primary");
          btn.setAttribute("data-g-pkg", pkg.name);
          btn.textContent = "Install " + pkg.family + " kernel (" + pkg.version + ")";
          slot.appendChild(btn);
        });
      });
    }

    function refreshPreview() {
      if (!getSignal("gPreviewPath")) return;
      var trigger = getEl("g-preview-trigger");
      if (trigger) trigger.click();
    }

    // Install the kernel this block needs, re-render the note so the block
    // becomes runnable, then run it — the click finishes what it started.
    function installForBlock(btn) {
      var block = btn.closest(".g-code-block");
      var id = block ? block.getAttribute("data-g-block") : null;
      btn.loading = true;
      call("kernel", "install", { name: btn.getAttribute("data-g-pkg") }).then(function (res) {
        btn.loading = false;
        if (!res) return;
        offers = null;
        refreshPreview();
        if (id === null) return;
        whenPresent('.g-code-block[data-g-block="' + id + '"] .g-code-run', function (run) { run.click(); });
      });
    }

    // Wait for an SSE-patched element to land, then act on it. Capped so a
    // re-render that never produces the button can't leave a timer running.
    function whenPresent(sel, fn, tries) {
      var el = document.querySelector(sel);
      if (el) { fn(el); return; }
      if ((tries || 0) >= 50) return;
      setTimeout(function () { whenPresent(sel, fn, (tries || 0) + 1); }, 100);
    }

    function remove(btn) {
      var kind = btn.getAttribute("data-g-kind");
      var id = btn.getAttribute("data-g-id");
      var body = kind === "kernel"
        ? { family: id, version: btn.getAttribute("data-g-version") }
        : { name: id };
      btn.loading = true;
      call(kind, "remove", body).then(function (res) {
        btn.loading = false;
        if (!res) return;
        if (kind === "theme") themePicker.remove(id);
        load(activePanel());
      });
    }


    function init() {
      var btn = getEl("g-extensions-btn");
      if (btn) btn.addEventListener("click", function () { open(null); });
      var input = getEl("g-ext-filter");
      if (input) input.addEventListener("sl-input", function () { resetWindows(); applyFilter(); });
      var tabs = getEl("g-ext-tabs");
      if (tabs) tabs.addEventListener("sl-tab-show", function (evt) { load(evt.detail.name); });
      // Rows are replaced wholesale by each list render, so re-apply the current
      // filter whenever they change — otherwise a fresh render shows every row.
      document.querySelectorAll("#g-extensions-dialog .g-ext-panel").forEach(function (panel) {
        new MutationObserver(applyFilter).observe(panel, { childList: true, subtree: true });
      });
      var dialog = getEl("g-extensions-dialog");
      if (dialog) dialog.addEventListener("click", function (e) {
        var add = e.target.closest(".g-ext-install");
        if (add) { install(add); return; }
        var del = e.target.closest(".g-ext-remove");
        if (del) { remove(del); return; }
        // The rest of an installed theme row activates it — same path as the
        // palette. The button branches above keep Remove clicks out. The row's
        // check follows by itself: it data-shows against $gTheme.
        var row = e.target.closest("[data-g-activate]");
        if (row) themePicker.apply(row.getAttribute("data-g-activate"));
      });

      // The install CTA lives inside note content, which is patched in per
      // preview and per failed run — so watch the preview body and delegate the
      // click on document rather than binding per block.
      var body = getEl("g-preview-body");
      if (body) {
        new MutationObserver(fillInstallSlots).observe(body, { childList: true, subtree: true });
        fillInstallSlots();
      }
      document.addEventListener("click", function (e) {
        var cta = e.target.closest(".g-code-install-btn");
        if (!cta) return;
        e.stopPropagation();
        installForBlock(cta);
      });
    }

    return { init: init, open: open };
  })();

  // Search: read the query box, push it into the gQuery signal (same proven path
  // as vault Save), bump gSeq so this turn gets fresh element ids, then fire the
  // trigger. sl-input's data-bind adapter is unreliable in the webview, so we
  // read .value in JS directly.
  var seq = 0;
  // initTrashSwitch persists the settings menu's trash switch. The checked state
  // is server-rendered from the persisted setting, so there's nothing to seed
  // here — plain JS rather than Datastar binding, whose adapter is unreliable for
  // Shoelace controls in the webview.
  function initTrashSwitch() {
    var sw = getEl("g-trash-switch");
    if (!sw) return;
    sw.addEventListener("sl-change", function () {
      fetch(apiURL("api/trash-enabled"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ gTrashEnabled: sw.checked }),
      }).catch(function () { /* persistence is best-effort. */ });
    });
  }

  // The search tuning bar (results, minimum relevance, this vault only) persists
  // per vault in the UI-state store, so switching vaults — a page load — keeps
  // what was set. The controls are Datastar signals, not data-bind inputs, so the
  // values are read off the DOM (as the graph's params() does). Saves are
  // debounced: a slider drag fires an input per pixel.
  var SEARCH_PARAMS_URL = "api/ui-state/search";
  function initSearchParams() {
    var k = getEl("g-search-k"), minSim = getEl("g-search-minsim"), thisVault = getEl("g-search-this-vault");
    if (!k || !minSim || !thisVault) return;
    var timer = null;
    function save() {
      if (timer) clearTimeout(timer);
      timer = setTimeout(function () {
        timer = null;
        fetch(apiURL(SEARCH_PARAMS_URL), {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ k: Number(k.value), minSim: Number(minSim.value), thisVault: !!thisVault.checked }),
        }).catch(function () { /* persistence is best-effort. */ });
      }, 400);
    }
    k.addEventListener("input", save);
    minSim.addEventListener("input", save);
    thisVault.addEventListener("change", save);
  }

  function initSearch() {
    var input = getEl("g-query-input");
    var search = getEl("g-search-btn");

    function run() {
      var q = input ? input.value.trim() : "";
      if (!q) return;
      seq += 1;
      setSignal("gQuery", q);
      setSignal("gSeq", String(seq));
      input.value = "";
      // Let the textarea recompute its auto-size so it shrinks back to one row.
      input.dispatchEvent(new Event("input", { bubbles: true }));
      // The results stream into the shared base panel, so make sure a session
      // tab is focused (and the preview/graph overlay is gone) — otherwise the
      // results render behind a note or the graph. The server records into the
      // active session (creating one on first use) and re-renders the list with it
      // marked active; adopt that row as the focused session tab.
      if (nav) nav.ensureSessionFocused();
      getEl("g-search-trigger").click();
      if (nav) nav.adoptActiveSession();
    }
    if (search) search.addEventListener("click", run);
    if (input) {
      // Enter searches; Shift+Enter inserts a newline (handled by the textarea).
      input.addEventListener("keydown", function (e) {
        if (e.key === "Enter" && !e.shiftKey) {
          e.preventDefault();
          run();
        }
      });
    }
  }

  // Drag the handle to resize the sidebar between min and max widths (MASS
  // Sidebar collapse (Obsidian-style): the header toggle folds the panel to a slim
  // rail and back, persisting the choice. A resize-set inline width would override
  // the collapsed CSS width, so it's stashed on collapse and restored on expand.
  var SIDEBAR_COLLAPSED_KEY = "grimoire.sidebarCollapsed";
  function initSidebarCollapse() {
    var panel = getEl("g-sidebar");
    var toggle = getEl("g-sidebar-toggle");
    if (!panel || !toggle) return;
    var savedWidth = "";

    var app = getEl("app-grimoire");
    function apply(collapsed) {
      if (collapsed) {
        savedWidth = panel.style.width; // remember a resized width to restore later.
        panel.style.width = "";
        panel.classList.add("g-collapsed");
        toggle.name = "layout-sidebar-inset-reverse";
        toggle.title = "Expand sidebar (Ctrl+B)";
      } else {
        panel.classList.remove("g-collapsed");
        if (savedWidth) panel.style.width = savedWidth;
        toggle.name = "layout-sidebar-inset";
        toggle.title = "Collapse sidebar (Ctrl+B)";
      }
      // Mirror on the root so the collapsed-layout rules (floated header, main-panel
      // offset + divider) can target the whole app.
      if (app) app.classList.toggle("g-sidebar-collapsed", collapsed);
    }

    function setCollapsed(collapsed) {
      apply(collapsed);
      try { localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed ? "1" : "0"); } catch (e) { /* ignore */ }
    }
    function toggleCollapsed() { setCollapsed(!panel.classList.contains("g-collapsed")); }

    try {
      if (localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "1") apply(true);
    } catch (e) { /* storage blocked; default to expanded */ }

    toggle.addEventListener("click", toggleCollapsed);

    // Ctrl/Cmd+B toggles the sidebar (the VS Code / common convention). Works even
    // while typing in a field — there's no bold-text editor to conflict with.
    document.addEventListener("keydown", function (e) {
      if ((e.ctrlKey || e.metaKey) && (e.key === "b" || e.key === "B")) {
        e.preventDefault();
        toggleCollapsed();
      }
    });
  }

  // pattern). Width is clamped; the bar highlights while dragging.
  function initResize() {
    var handle = getEl("g-resize-handle");
    var panel = getEl("g-sidebar");
    var bar = getEl("g-resize-bar");
    if (!handle || !panel) return;
    var MIN = 220, MAX = 600;
    var startX = 0, startW = 0, dragging = false;

    handle.addEventListener("mousedown", function (e) {
      dragging = true;
      startX = e.clientX;
      startW = panel.offsetWidth;
      document.body.style.cursor = "col-resize";
      document.body.style.userSelect = "none";
      if (bar) bar.style.background = "var(--mass-accent)";
      e.preventDefault();
    });
    document.addEventListener("mousemove", function (e) {
      if (!dragging) return;
      var w = Math.max(MIN, Math.min(MAX, startW + e.clientX - startX));
      panel.style.width = w + "px";
      e.preventDefault();
    });
    document.addEventListener("mouseup", function () {
      if (!dragging) return;
      dragging = false;
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
      if (bar) bar.style.background = "";
    });
  }

  // Sessions: single-click a row to open it (instantly), double-click anywhere on
  // the row to rename inline, × to delete. Opening a session re-renders the list,
  // which wipes a rename input mid-double-click — so a double-click re-applies the
  // rename to the row once it reappears (pendingRenameId, below).
  // confirmDelete shows the shared delete dialog with a label and message, then
  // runs onConfirm if the user confirms. One dialog backs every delete (notes,
  // folders, sessions) so they all get the same confirmation. Wired once by
  // initConfirmDelete; ready returns false until then.
  var confirmDelete = (function () {
    var dialog, bodyEl, onConfirm = null;
    function run() {
      if (dialog) dialog.hide();
      var fn = onConfirm;
      onConfirm = null;
      if (fn) fn();
    }
    function init() {
      dialog = getEl("g-delete-dialog");
      bodyEl = getEl("g-delete-body");
      if (!dialog) return;
      getEl("g-delete-cancel").addEventListener("click", function () { dialog.hide(); });
      getEl("g-delete-confirm").addEventListener("click", run);
      // Enter confirms while the dialog is open (Esc cancels). Esc is handled in
      // capture phase on the document so it just closes the dialog and stops there
      // — otherwise it bubbles to the preview's Esc handler, closing the view
      // behind the dialog. Esc does one thing at a time: while the dialog is open
      // it only cancels the dialog.
      dialog.addEventListener("keydown", function (e) {
        if (e.key === "Enter") { e.preventDefault(); run(); }
      });
      document.addEventListener("keydown", function (e) {
        if (e.key === "Escape" && dialog.open) {
          e.preventDefault();
          // stopImmediatePropagation, not stopPropagation: the other Esc layers
          // (trash exit, selection clear) are sibling listeners on document in the
          // same capture phase — plain stopPropagation wouldn't stop them, so the
          // trash view would still close behind the dialog.
          e.stopImmediatePropagation();
          dialog.hide();
        }
      }, true);
    }
    function ask(label, message, fn) {
      if (!dialog) return;
      onConfirm = fn;
      dialog.label = label;
      if (bodyEl) bodyEl.textContent = message;
      dialog.show();
    }
    return { init: init, ask: ask };
  })();

  function initSessions() {
    var newBtn = getEl("g-new-session");
    var list = getEl("g-sessions");
    if (newBtn) {
      // New session opens a blank scratch tab (same as the strip "+"); it commits
      // nothing — asking in it creates the real session server-side. This avoids
      // piling up empty "New session" rows just from pressing the button.
      newBtn.addEventListener("click", function () { if (nav) nav.openScratch(); });
    }
    // The book icon is a Home button: back to the base empty prompt.
    var home = getEl("g-home");
    if (home) home.addEventListener("click", function () { if (nav) nav.home(); });
    if (!list) return;

    // IDE-style open: single-click previews a session in its reusable preview tab;
    // double-click opens it as a permanent (pinned) tab. e.detail skips the first
    // click of a double-click so the dblclick handler does the pinned open instead
    // of leaving a preview behind.
    function openRow(row, pinned) {
      if (!nav) return;
      var fn = pinned ? nav.openSessionPinned : nav.openSession;
      fn(row.getAttribute("data-id"), row.getAttribute("data-open-url"), row.getAttribute("data-title"));
    }

    list.addEventListener("click", function (e) {
      var del = e.target.closest(".g-session-del");
      if (del) {
        e.stopPropagation();
        var delRow = del.closest(".g-session");
        if (delRow) deleteSession(delRow);
        return;
      }
      var row = e.target.closest(".g-session");
      if (!row || row.querySelector(".g-session-edit")) return; // mid-rename.
      // Ctrl/Shift+click builds a multi-selection (handled in keyboard nav) — don't
      // also open the session.
      if (e.ctrlKey || e.metaKey || e.shiftKey) return;
      if (e.detail > 1) return; // second click of a double-click → dblclick pins it.
      openRow(row, false);
    });

    list.addEventListener("dblclick", function (e) {
      var row = e.target.closest(".g-session");
      if (row && !row.querySelector(".g-session-edit")) openRow(row, true);
    });

    // Right-click renames the row inline (IDE convention). The open above re-renders
    // the list, replacing the row and any input we placed, so we remember the id and
    // re-apply the rename once the row reappears.
    var pendingRenameId = null;
    list.addEventListener("contextmenu", function (e) {
      var row = e.target.closest(".g-session");
      if (!row || row.querySelector(".g-session-edit")) return;
      e.preventDefault();
      pendingRenameId = row.getAttribute("data-id");
      tryPendingRename();
    });

    // Re-apply a pending rename after the list re-renders. Cleared once the input is
    // placed; startInlineRename clears it on commit/cancel too.
    function tryPendingRename() {
      if (!pendingRenameId) return;
      var row = list.querySelector('.g-session[data-id="' + cssEscape(pendingRenameId) + '"]');
      if (row && !row.querySelector(".g-session-edit")) {
        startInlineRename(row, function () { pendingRenameId = null; });
      }
    }
    new MutationObserver(tryPendingRename).observe(list, { childList: true });

    // Delete key removes the keyboard-selected, hovered, or otherwise the active
    // (open) session, mirroring the file tree. Ignored while typing — except in the
    // list filter, where an arrow-selected row should still be deletable — and
    // never mid-rename.
    var hoverRow = null;
    list.addEventListener("mouseover", function (e) { hoverRow = e.target.closest(".g-session"); });
    list.addEventListener("mouseleave", function () { hoverRow = null; });
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Delete" && e.key !== "Del") return;
      if (typing(e.target) && !inListFilter(e.target)) return;
      // A multi-selection takes the whole batch (shared with the action bar's
      // Delete button); otherwise delete the single selected/hovered/active row.
      if (batchDeleteSelection && batchDeleteSelection(list)) { e.preventDefault(); return; }
      var row = list.querySelector(".g-kbd-sel") || hoverRow || list.querySelector(".g-session-active");
      if (row && !row.querySelector(".g-session-edit")) { e.preventDefault(); deleteSession(row); }
    });

    initSessionFilter(list);
  }

  // deleteSession asks for confirmation, then removes the session: set its id
  // signal and fire the hidden delete trigger (deferred so Datastar commits the
  // signal before @post).
  function deleteSession(row) {
    var name = row.getAttribute("data-title") || "this session";
    var id = row.getAttribute("data-id");
    confirmDelete.ask("Delete session", 'Delete "' + name + '"? This permanently removes the session.', function () {
      fireWithSignal("gSessionID", id, "g-session-delete-trigger");
      if (nav) nav.closeSession(id); // drop its workspace tab, if open.
    });
  }

  // Filter the session list by title, client-side. The list re-renders from the
  // server after every turn/rename/delete, so we re-apply the current filter
  // whenever its rows change (a MutationObserver on the list) — otherwise a fresh
  // render would show hidden sessions again.
  function initSessionFilter(list) {
    var filter = getEl("g-session-filter");
    if (!filter) return;
    function apply() {
      var q = filter.value;
      var rows = list.querySelectorAll(".g-session");
      for (var i = 0; i < rows.length; i++) {
        var title = rows[i].getAttribute("data-title") || "";
        rows[i].style.display = matchesFilter(title, q) ? "" : "none";
      }
    }
    filter.addEventListener("sl-input", apply);
    new MutationObserver(apply).observe(list, { childList: true });
  }

  function startInlineRename(row, onDone) {
    var titleEl = row.querySelector(".g-session-title");
    if (!titleEl) return;
    var current = row.getAttribute("data-title");
    var input = document.createElement("input");
    input.type = "text";
    input.className = "g-session-edit";
    input.autocomplete = "off";
    input.value = current;
    titleEl.replaceWith(input);
    input.focus();
    input.select();

    var done = false;
    function commit(save) {
      if (done) return;
      done = true;
      if (onDone) onDone();
      var next = input.value.trim();
      if (save && next !== "" && next !== current) {
        setSignal("gSessionTitle", next);
        fireWithSignal("gSessionID", row.getAttribute("data-id"), "g-session-rename-trigger");
      } else {
        // Re-render from the server to restore the row as it was.
        getEl("g-sessions-trigger").click();
      }
    }
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); commit(true); }
      else if (e.key === "Escape") { e.preventDefault(); commit(false); }
    });
    input.addEventListener("blur", function () { commit(true); });
  }

  // Files: filter the vault tree by note name. Hides non-matching notes and any
  // folder left with no visible notes; clears back to the full tree when empty.
  // The tree re-renders from the server on load, so re-apply on its mutations.
  function initFiles() {
    var filter = getEl("g-files-filter");
    var tree = getEl("g-files");
    if (!filter || !tree) return;
    var savedOpen = null; // fold state captured when filtering begins.

    function apply() {
      var q = (filter.value || "").trim().toLowerCase();
      var folders = tree.querySelectorAll(".g-tree-folder");
      if (!q) {
        // Filter cleared: show everything and restore the manual fold state so a
        // filter doesn't leave every folder unfolded.
        tree.querySelectorAll(".g-tree-note,.g-tree-other").forEach(function (r) { r.style.display = ""; });
        folders.forEach(function (f) { f.style.display = ""; });
        if (savedOpen) {
          folders.forEach(function (f, i) { f.open = savedOpen[i]; });
          savedOpen = null;
        }
        return;
      }
      // Entering filter mode: remember how folders were folded, to restore later.
      if (!savedOpen) {
        savedOpen = Array.prototype.map.call(folders, function (f) { return f.open; });
      }
      // Filter leaf rows by name, plus a note's tags and aliases. Each query word
      // must appear somewhere in that combined haystack (any order).
      tree.querySelectorAll(".g-tree-note,.g-tree-other").forEach(function (row) {
        var hay = [
          row.getAttribute("data-name") || "",
          row.getAttribute("data-tags") || "",
          row.getAttribute("data-aliases") || "",
        ].join(" ");
        row.style.display = matchesFilter(hay, q) ? "" : "none";
      });
      // Unfold exactly the folders on a path to a match (so matches are visible)
      // and hide folders with no match. The pre-filter fold state is restored
      // when the filter clears.
      folders.forEach(function (folder) {
        var hasVisible = Array.prototype.some.call(
          folder.querySelectorAll(".g-tree-note,.g-tree-other"),
          function (n) { return n.style.display !== "none"; }
        );
        folder.style.display = hasVisible ? "" : "none";
        folder.open = hasVisible;
      });
    }
    filter.addEventListener("sl-input", apply);
    // Re-apply after the tree re-renders (e.g. reindex), preserving the filter.
    new MutationObserver(function () { savedOpen = null; apply(); }).observe(tree, { childList: true });
  }

  // Mark the open note's row as active (filled, like the active session). The
  // server-rendered tree doesn't know what's open, so it's marked client-side from
  // the gPreviewPath signal and re-applied whenever the tree re-renders or the
  // preview opens/closes. Cleared when the preview isn't showing a note.
  function initActiveNote() {
    var tree = getEl("g-files");
    var preview = getEl("g-preview");
    if (!tree || !preview) return;

    // The active tree row tracks the FOCUSED tab: lit only when a note tab is
    // focused (its path), cleared for session/graph/empty. nav.focusedNotePath()
    // is the single source of truth, so the highlight can't disagree with the view.
    function mark() {
      var path = nav ? nav.focusedNotePath() : "";
      tree.querySelectorAll(".g-tree-note-active").forEach(function (r) {
        if (!path || r.getAttribute("data-note") !== path) r.classList.remove("g-tree-note-active");
      });
      if (!path) return;
      var row = tree.querySelector('.g-tree-note[data-note="' + cssEscape(path) + '"]');
      if (row) row.classList.add("g-tree-note-active");
    }

    new MutationObserver(mark).observe(tree, { childList: true, subtree: true });
    // The preview's display flips with gPreviewOpen (data-show) and its body
    // changes when a different note loads — both are cues to re-mark.
    new MutationObserver(mark).observe(preview, { attributes: true, attributeFilter: ["style"] });
    var pbody = getEl("g-preview-body");
    if (pbody) new MutationObserver(mark).observe(pbody, { childList: true });
    mark();
  }

  // Keep Tab focus on Grimoire's own chrome (toolbar, search, tabs, settings) and
  // OUT of the viewed note. The preview panel — its header icons, find bar, the
  // properties inputs, the body's wikilinks, and the raw-Markdown editor — is the
  // content you're reading, not app controls, so it shouldn't sit in the tab order.
  // The body and properties are re-rendered by the server on every note load, so a
  // MutationObserver re-stamps tabindex="-1" on any focusable descendant rather than
  // us chasing each re-render. Clicking still focuses these elements (mouse focus
  // ignores tabindex<0); only keyboard Tab skips them.
  function initPreviewUntabbable() {
    var preview = getEl("g-preview");
    if (!preview) return;
    // Both native focusables and Shoelace components that expose a tab stop.
    var FOCUSABLE =
      "a[href],input,textarea,select,button," +
      "sl-icon-button,sl-button,sl-input,sl-select,sl-textarea,sl-range,sl-switch";
    function strip() {
      preview.querySelectorAll(FOCUSABLE).forEach(function (el) {
        if (el.getAttribute("tabindex") !== "-1") el.setAttribute("tabindex", "-1");
      });
    }
    new MutationObserver(strip).observe(preview, { childList: true, subtree: true });
    strip();
  }

  // Drag-to-move: drag a note or folder row onto a folder (or empty tree space =
  // vault root) to move it there. Dragging a row that's part of the multi-selection
  // moves the whole selection. A custom dataTransfer type (DRAG_MIME) marks the
  // drag as internal so the OS file-import dropzone ignores it (it keys off the
  // "Files" type). The server guards against moving a folder into its own subtree
  // and against name collisions, so the client only filters obvious no-ops.
  var DRAG_MIME = "application/x-grimoire-move";
  function initDragMove() {
    var tree = getEl("g-files");
    if (!tree) return;
    var payload = null;     // { notes: [paths], folders: [paths] } for the active drag.
    var lastTarget = null;  // the row/tree currently ringed as the drop target.

    // dragRows returns the rows to move: the whole multi-selection when the dragged
    // row is in it, else just the dragged row.
    function dragRows(row) {
      if (row.classList.contains("g-multi-sel")) {
        return Array.prototype.slice.call(tree.querySelectorAll(".g-multi-sel"));
      }
      return [row];
    }
    function buildPayload(rows) {
      var notes = [], folders = [];
      rows.forEach(function (r) {
        if (r.classList.contains("g-tree-note")) notes.push(r.getAttribute("data-note"));
        else if (r.classList.contains("g-tree-folder-row")) folders.push(r.getAttribute("data-folder"));
      });
      return { notes: notes, folders: folders };
    }

    tree.addEventListener("dragstart", function (e) {
      var row = e.target.closest(".g-tree-note,.g-tree-folder-row");
      if (!row) return;
      var rows = dragRows(row);
      payload = buildPayload(rows);
      rows.forEach(function (r) { r.classList.add("g-dragging"); });
      // Mark the drag internal (so import ignores it) and carry a label for the OS
      // drag image. setData is required for Firefox to start the drag at all.
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData(DRAG_MIME, "1");
      e.dataTransfer.setData("text/plain", row.getAttribute("data-name") || "");
    });
    tree.addEventListener("dragend", function () {
      tree.querySelectorAll(".g-dragging").forEach(function (r) { r.classList.remove("g-dragging"); });
      clearTarget();
      payload = null;
    });

    function internal(e) {
      return e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types || [], DRAG_MIME) !== -1;
    }
    function clearTarget() {
      if (lastTarget) { lastTarget.classList.remove("g-drop-target"); lastTarget = null; }
    }
    // targetFor returns the drop target element (a folder row or the tree itself for
    // the vault root) and its destination folder path ("" = root).
    function targetFor(e) {
      var folderRow = e.target.closest(".g-tree-folder-row");
      if (folderRow) return { el: folderRow, dest: folderRow.getAttribute("data-folder") };
      return { el: tree, dest: "" }; // empty tree space → vault root.
    }

    tree.addEventListener("dragover", function (e) {
      if (!internal(e) || !payload) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      var t = targetFor(e);
      if (t.el !== lastTarget) { clearTarget(); lastTarget = t.el; t.el.classList.add("g-drop-target"); }
    });
    tree.addEventListener("dragleave", function (e) {
      // Only clear when leaving the tree entirely (not when crossing child rows).
      if (!tree.contains(e.relatedTarget)) clearTarget();
    });
    tree.addEventListener("drop", function (e) {
      if (!internal(e) || !payload) return;
      e.preventDefault();
      var dest = targetFor(e).dest;
      clearTarget();
      doMove(payload, dest);
      payload = null;
    });

    // doMove drops no-ops (an entry already in dest, or a folder dropped into its
    // own subtree) client-side, then posts the rest; the server re-checks too.
    function doMove(p, dest) {
      var notes = p.notes.filter(function (path) { return parentOf(path) !== dest; });
      var folders = p.folders.filter(function (path) {
        return parentOf(path) !== dest && dest !== path && dest.indexOf(path + "/") !== 0;
      });
      if (!notes.length && !folders.length) return; // nothing actually moves.
      setSignal("gBatchPaths", JSON.stringify(notes));
      setSignal("gBatchFolders", JSON.stringify(folders));
      fireWithSignal("gMoveTarget", dest, "g-move-trigger");
      // Rebind any open tabs for the moved notes/folders so they don't go stale.
      if (nav) {
        var base = dest ? dest + "/" : "";
        notes.forEach(function (p) { nav.renameNote(p, base + p.slice(p.lastIndexOf("/") + 1)); });
        folders.forEach(function (p) { nav.renameNotesUnder(p, base + p.slice(p.lastIndexOf("/") + 1)); });
      }
    }
    function parentOf(path) {
      var i = path.lastIndexOf("/");
      return i < 0 ? "" : path.slice(0, i);
    }
  }

  // File actions: the Obsidian-style toolbar (new note, new folder, sort, collapse
  // all), delete-with-confirm, and double-click inline rename — the create/delete/
  // rename write paths over the vault tree.
  function initFileActions() {
    var tree = getEl("g-files");
    if (!tree) return;

    var folds = initFolderState(tree);

    // Toolbar: new note / new folder create on the server (which re-renders the
    // tree and signals the new path); sort and collapse are client-side. The
    // toolbar creates at the vault root (empty parent).
    var newNote = getEl("g-new-note");
    if (newNote) newNote.addEventListener("click", function () { createInto("", "g-note-create-trigger"); });
    var newFolder = getEl("g-new-folder");
    if (newFolder) newFolder.addEventListener("click", function () { createInto("", "g-folder-create-trigger"); });

    // A folder row's hover "+" buttons create inside that folder. stopPropagation
    // so the click doesn't toggle the <details>.
    tree.addEventListener("click", function (e) {
      var addNote = e.target.closest(".g-tree-add-note");
      var addFolder = e.target.closest(".g-tree-add-folder");
      if (!addNote && !addFolder) return;
      e.stopPropagation();
      e.preventDefault();
      var row = (addNote || addFolder).closest(".g-tree-folder-row");
      var parent = row ? row.getAttribute("data-folder") : "";
      if (parent) folds.add(parent); // reveal the folder so the new row shows.
      createInto(parent, addNote ? "g-note-create-trigger" : "g-folder-create-trigger");
    }, true);

    initFilesImport(tree, folds);
    initFilesSort(tree);
    var collapse = getEl("g-files-collapse");
    if (collapse) {
      collapse.addEventListener("click", function () {
        folds.clear();
        tree.querySelectorAll(".g-tree-folder").forEach(function (f) { f.open = false; });
      });
    }

    // After a tree re-render, reveal a freshly-created note (open + rename) or a
    // freshly-created folder (rename in place). The signal is set before the tree
    // re-renders, so it's present when this observer fires. A row nested in a
    // collapsed folder is opened up to so the rename input is visible.
    new MutationObserver(function () {
      var note = getSignal("gNewNote");
      if (note) {
        setSignal("gNewNote", ""); // consume so a later re-render doesn't re-open.
        var noteRow = tree.querySelector('.g-tree-note[data-note="' + cssEscape(note) + '"]');
        if (noteRow) { openAncestors(noteRow, folds); noteRow.click(); startRowRename(noteRow); }
        return;
      }
      var folder = getSignal("gNewFolder");
      if (folder) {
        setSignal("gNewFolder", "");
        var folderRow = tree.querySelector('.g-tree-folder-row[data-folder="' + cssEscape(folder) + '"]');
        if (folderRow) { openAncestors(folderRow, folds); startRowRename(folderRow); }
      }
    }).observe(tree, { childList: true });

    // Set the create target and fire the create trigger on the next frame, so
    // Datastar commits gNewParent before the @post serializes it.
    function createInto(parent, triggerId) {
      fireWithSignal("gNewParent", parent, triggerId);
    }

    // Delete: the trash on a note or folder row asks for confirmation, then fires
    // the matching delete. A folder delete is recursive, so its message warns
    // about contents.
    function askDelete(row) {
      if (!row) return;
      var isFolder = row.classList.contains("g-tree-folder-row");
      var path = row.getAttribute(isFolder ? "data-folder" : "data-note");
      var name = row.getAttribute("data-name") || path;
      var message = isFolder
        ? 'Delete "' + name + '" and everything inside it? This removes the folder and all its contents from your vault on disk.'
        : 'Delete "' + name + '"? This removes it from your vault on disk.';
      confirmDelete.ask(isFolder ? "Delete folder" : "Delete note", message, function () {
        if (isFolder) { fireWithSignal("gFolderPath", path, "g-folder-delete-trigger"); if (nav) nav.closeNotesUnder(path); }
        else { fireWithSignal("gNotePath", path, "g-note-delete-trigger"); if (nav) nav.closeNote(path); }
      });
    }
    tree.addEventListener("click", function (e) {
      var del = e.target.closest(".g-tree-del,.g-tree-del-folder");
      if (!del) return;
      e.stopPropagation(); // don't open the note / toggle the folder.
      e.preventDefault();
      askDelete(del.closest(".g-tree-note,.g-tree-folder-row"));
    }, true);

    // IDE-style: single-click a note previews it (the document [data-note] handler);
    // double-click opens it as a permanent (pinned) tab. A folder has no tab, so its
    // double-click renames in place (its single-click toggles the folder natively).
    tree.addEventListener("dblclick", function (e) {
      var row = e.target.closest(".g-tree-note,.g-tree-folder-row");
      if (!row) return;
      e.preventDefault();
      if (row.classList.contains("g-tree-note")) {
        if (nav) nav.openNotePinned(row.getAttribute("data-note"), "");
      } else {
        startRowRename(row);
      }
    });

    // Right-click renames a note or folder row inline (IDE convention).
    tree.addEventListener("contextmenu", function (e) {
      var row = e.target.closest(".g-tree-note,.g-tree-folder-row");
      if (row) { e.preventDefault(); startRowRename(row); }
    });

    // Del deletes the keyboard-selected (arrow nav) note or folder, falling back to
    // the row under the cursor, then the open note — same as sessions. Ignored
    // while typing — except in the notes filter, where an arrow-selected row stays
    // deletable — and when the confirm dialog is already up.
    var hoverRow = null;
    tree.addEventListener("mouseover", function (e) {
      hoverRow = e.target.closest(".g-tree-note,.g-tree-folder-row");
    });
    tree.addEventListener("mouseleave", function () { hoverRow = null; });
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Delete" && e.key !== "Del") return;
      if (typing(e.target) && !inListFilter(e.target)) return;
      var dlg = getEl("g-delete-dialog");
      if (dlg && dlg.open) return; // confirm already up.
      // A multi-selection takes the whole batch (shared with the action bar's
      // Delete button); otherwise delete the single selected/hovered/open row.
      if (batchDeleteSelection && batchDeleteSelection(tree)) { e.preventDefault(); return; }
      var row = tree.querySelector(".g-kbd-sel") || hoverRow || tree.querySelector(".g-tree-note-active");
      if (row) { e.preventDefault(); askDelete(row); }
    });
  }

  // Trash: the toolbar Trash button toggles the Files view between the vault tree
  // and the trash, both rendered into #g-files by the server — the trash is just
  // the file view "opened in a special folder", so it reuses every bit of the file
  // view (preview, open-pinned, multi-select, keyboard nav). A class on the section
  // (g-files-trashing) flips the action-bar labels and reveals Restore. Per-row
  // restore/delete each post directly via the server-inlined data-on:click (the id
  // is in the URL — no client signal to race), with delete-forever and Empty gated
  // by a confirm dialog first.
  function initTrash() {
    var section = getEl("g-files-section");
    var btn = getEl("g-files-trash");
    var files = getEl("g-files");
    var filter = getEl("g-files-filter");
    if (!section || !btn || !files) return;

    function inTrashMode() { return section.classList.contains("g-files-trashing"); }

    function setTrashMode(on) {
      if (on === inTrashMode()) return;
      section.classList.toggle("g-files-trashing", on);
      if (filter) filter.placeholder = on ? "Filter trash…" : "Filter notes (any word order)…";
      if (clearFilesSelection) clearFilesSelection(); // don't carry a selection across the mode change.
      // Re-render #g-files for the new mode: the trash list, or back to the tree.
      var trigger = getEl(on ? "g-trash-open-trigger" : "g-trash-close-trigger");
      if (trigger) trigger.click();
    }

    // Toolbar Trash button toggles trash mode.
    btn.addEventListener("click", function () { setTrashMode(!inTrashMode()); });

    // Esc exits the trash view — exposed to the layered Esc handler, which calls
    // this as its last layer (after clearing any selection). Returns true if it
    // actually left the trash, so Esc knows it consumed the key.
    exitTrashMode = function () {
      if (!inTrashMode()) return false;
      setTrashMode(false);
      return true;
    };

    // Empty trash (footer service zone, trash mode only).
    var emptyBtn = getEl("g-files-trash-empty");
    if (emptyBtn) emptyBtn.addEventListener("click", function () {
      confirmDelete.ask("Empty trash",
        "Permanently delete everything in the trash? This cannot be undone.",
        function () {
          requestAnimationFrame(function () {
            var t = getEl("g-trash-empty-trigger");
            if (t) t.click();
          });
        });
    });

    // Delete-forever confirm-gate: the row button posts directly (data-on:click),
    // so intercept it in the CAPTURE phase to confirm first; on confirm, re-fire a
    // click that's let through (the in-flight flag). Restore needs no gate. Bound
    // to #g-files since the trash rows render there.
    var allowDel = false;
    files.addEventListener("click", function (e) {
      var del = e.target.closest(".g-trash-del");
      if (!del || allowDel) { allowDel = false; return; }
      e.preventDefault();
      e.stopPropagation();
      var name = del.getAttribute("data-trash-name") || "this note";
      confirmDelete.ask("Delete permanently",
        'Permanently delete "' + name + '"? This removes it from the trash for good and cannot be undone.',
        function () { allowDel = true; del.click(); });
    }, true);
  }

  // Import: drag .md / .txt files onto the Files tab (or click the toolbar import
  // button) to add them to the vault as notes. Mirrors pdf2doc's dropzone: each
  // file's bytes are read and POSTed as the raw request body, its name riding in
  // the X-Filename header (the server maps .txt → .md and de-dupes collisions).
  // Once every file lands the tree is repainted from the server.
  function initFilesImport(tree, folds) {
    var section = getEl("g-files-section");
    var input = getEl("g-files-input");
    var importBtn = getEl("g-files-import");
    if (!section || !input) return;

    // A drop nothing claims makes WebKit navigate to the dropped file,
    // replacing the whole app with its viewer (only a restart recovers).
    // Deny the default everywhere; the section handlers below (which run
    // first, on the bubble path up) opt in to real imports.
    document.addEventListener("dragover", function (e) { e.preventDefault(); });
    document.addEventListener("drop", function (e) { e.preventDefault(); });

    // Clicking the toolbar button opens the native file picker.
    if (importBtn) importBtn.addEventListener("click", function () { input.click(); });
    input.addEventListener("change", function () {
      importFiles(input.files);
      input.value = ""; // allow re-picking the same file.
    });

    // Drag highlight: the dragover class reveals the overlay. dragleave only
    // clears when the pointer truly leaves the section (relatedTarget is outside),
    // so moving over child rows doesn't flicker it off.
    section.addEventListener("dragover", function (e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      section.classList.add("dragover");
    });
    section.addEventListener("dragleave", function (e) {
      if (!section.contains(e.relatedTarget)) section.classList.remove("dragover");
    });
    section.addEventListener("drop", function (e) {
      e.preventDefault();
      section.classList.remove("dragover");
      if (!e.dataTransfer) return;
      if (e.dataTransfer.files && e.dataTransfer.files.length) importFiles(e.dataTransfer.files);
      // A file drag whose files WebKit didn't hand over (seen with some
      // GTK drag sources) — say so instead of silently doing nothing.
      else if (hasFiles(e)) showNotice("Couldn't read the dropped files — use the import button instead.");
    });

    function hasFiles(e) {
      var t = e.dataTransfer;
      if (!t) return false;
      var types = Array.prototype.slice.call(t.types || []);
      if (types.indexOf(DRAG_MIME) !== -1) return false; // internal row move, not an OS drag
      // Files dragged from the OS show up as "Files"; WebKitGTK sometimes
      // reports only "text/uri-list" during dragover.
      return types.indexOf("Files") !== -1 || types.indexOf("text/uri-list") !== -1;
    }

    // Upload accepted files with bounded concurrency (the Index concurrency
    // setting), then repaint the tree once. The server's O_EXCL de-dup is
    // race-safe, so parallel uploads of distinct files are safe and faster for a
    // big drop (office docs convert + embed server-side). Progress counts
    // completed files; the final tree re-render refreshes the chunk count. Files
    // we can't take are reported, not silently dropped.
    // importAbort is the controller for the in-flight import (null when idle).
    // Aborting it cancels the running uploads — for a PDF the server's request
    // context is cancelled, so the page-by-page conversion stops — and prevents
    // any not-yet-started file from being sent.
    var importAbort = null;

    function importFiles(fileList) {
      var all = Array.prototype.slice.call(fileList || []);
      var files = all.filter(accepted);
      var skipped = all.filter(function (f) { return !accepted(f); });
      if (files.length === 0) {
        if (skipped.length) notifySkipped(skipped);
        return;
      }
      var total = files.length, next = 0, done = 0, failed = 0;
      var reasons = []; // server-supplied failure messages, deduped for display.
      var limit = Math.max(1, Math.min(total, importLimit()));
      var ctrl = new AbortController();
      var finished = false; // guard finish() against the racing worker chains.
      importAbort = ctrl;

      function note(reason) {
        if (reason && reasons.indexOf(reason) === -1) reasons.push(reason);
      }

      function finish() {
        if (finished) return;
        finished = true;
        importAbort = null;
        getEl("g-files-trigger").click(); // repaint the tree (any landed files).
        if (ctrl.signal.aborted) finishCancelled();
        else finishImport(total, failed, skipped, reasons);
      }

      function startOne() {
        if (next >= total || ctrl.signal.aborted) return;
        var file = files[next++];
        file.arrayBuffer().then(function (buf) {
          return fetch(apiURL("api/note/import"), {
            method: "POST",
            headers: { "X-Filename": encodeURIComponent(file.name), "X-Parent": "" },
            body: buf,
            signal: ctrl.signal,
          }).then(function (resp) {
            if (resp.ok) return;
            failed++;
            return resp.text().then(function (t) { note((t || "").trim()); });
          });
        }).catch(function (e) {
          if (e && e.name === "AbortError") return; // cancelled, not a failure.
          failed++;
          note("network error");
        }).finally(function () {
          done++;
          if (ctrl.signal.aborted) { finish(); return; }
          showProgress(done, total, file.name);
          if (next < total) startOne();
          else if (done >= total) finish();
        });
      }
      showProgress(0, total, files[0].name);
      for (var w = 0; w < limit; w++) startOne();
    }

    // importLimit reads the configured Index concurrency, so a drop uploads as
    // many files at once as a reindex embeds. Falls back to a small default.
    function importLimit() {
      var n = parseInt(getSignal("gConcurrency"), 10);
      return n > 0 ? n : 6;
    }

    function accepted(file) {
      return /\.(md|markdown|txt|html?|docx|odt|pdf)$/i.test(file.name);
    }

    // Import status lives in a slim, non-blocking line above the tree (the tree
    // stays browsable during import), not the drag overlay — so a slow multi-file
    // import doesn't block navigation. The line shows a spinner + current file
    // while importing, a progress bar when more than one file, and a dismissable
    // warning when something failed or was unsupported.
    var status = getEl("g-files-status");
    var statusText = getEl("g-files-status-text");
    var statusFill = getEl("g-files-status-fill");
    var statusClose = getEl("g-files-status-close");
    var statusTimer = null;
    var NOTICE_MAX_NAMES = 5; // cap the listed names so a big drop can't overflow.

    function clearStatus() {
      if (!status) return;
      if (statusTimer) { clearTimeout(statusTimer); statusTimer = null; }
      status.className = "g-files-status";
      if (statusFill) statusFill.style.width = "0";
    }

    // Show import progress: which file is being sent, of how many, with the bar
    // filled by completed count when a drop brings more than one file.
    // showProgress reflects how many of total files have finished importing (done)
    // and the most recent filename. The bar fills by completed fraction; multi-file
    // imports get the bar, a single file just the spinner + name.
    function showProgress(done, total, name) {
      if (!status) return;
      if (statusTimer) { clearTimeout(statusTimer); statusTimer = null; }
      status.className = "g-files-status g-files-status-active" + (total > 1 ? " g-files-status-multi" : "");
      if (statusClose) statusClose.title = "Cancel import";
      if (statusText) statusText.textContent = total > 1 ? "Importing " + done + " of " + total + "… " + name : "Importing… " + name;
      if (statusFill && total > 1) statusFill.style.width = Math.round((done / total) * 100) + "%";
    }

    // Wrap up an import: report any failed or unsupported files as a dismissable
    // warning; a fully clean import just hides the line after a short confirmation.
    function finishImport(total, failed, skipped, reasons) {
      if (!status) return;
      if (statusClose) statusClose.title = "Dismiss";
      if (statusFill) statusFill.style.width = "100%";
      if (failed > 0 || skipped.length > 0) {
        var parts = [];
        if (failed > 0) {
          var head = failed + " of " + total + " failed";
          if (reasons && reasons.length) head += ": " + reasons.join("; ");
          parts.push(head);
        }
        if (skipped.length > 0) parts.push(skipped.length + " unsupported (.md, .txt, .html, .docx, .odt, .pdf only): " + listNames(skipped));
        showNotice(parts.join(" · "));
      } else {
        if (statusText) statusText.textContent = "Imported " + total + " file" + (total === 1 ? "" : "s");
        status.className = "g-files-status g-files-status-active g-files-status-done";
        statusTimer = setTimeout(clearStatus, 2500);
      }
    }

    // finishCancelled reports a user-cancelled import; any files that already
    // landed before the cancel are kept (the tree repaint shows them).
    function finishCancelled() {
      if (!status) return;
      if (statusClose) statusClose.title = "Dismiss";
      if (statusText) { statusText.textContent = "Import cancelled"; statusText.title = ""; }
      status.className = "g-files-status g-files-status-active g-files-status-done";
      if (statusTimer) clearTimeout(statusTimer);
      statusTimer = setTimeout(clearStatus, 2500);
    }

    function notifySkipped(skipped) {
      showNotice("Can't import " + skipped.length + " file" + (skipped.length === 1 ? "" : "s") +
        " — supported: .md, .txt, .html, .docx, .odt, .pdf: " + listNames(skipped));
    }

    function listNames(files) {
      var names = files.slice(0, NOTICE_MAX_NAMES).map(function (f) { return f.name; }).join(", ");
      if (files.length > NOTICE_MAX_NAMES) names += ", +" + (files.length - NOTICE_MAX_NAMES) + " more";
      return names;
    }

    // Native bridge: on Linux the webview delivers OS file drops natively
    // (WebKitGTK never lets the DOM see an external file drag) and the Go
    // side imports the files itself; these hooks let it drive the same
    // status line as the in-page dropzone.
    window.gNativeImportProgress = showProgress;
    window.gNativeImportNotice = showNotice;
    window.gNativeImportDone = function (total, failed, skipped, reasons) {
      getEl("g-files-trigger").click(); // repaint the tree (any landed files).
      finishImport(total, failed, (skipped || []).map(function (n) { return { name: n }; }), reasons || []);
    };

    // Show a dismissable warning in the status line; auto-clears after a few sec.
    function showNotice(text) {
      if (!status) return;
      if (statusClose) statusClose.title = "Dismiss";
      if (statusText) { statusText.textContent = text; statusText.title = text; }
      status.className = "g-files-status g-files-status-active g-files-status-done g-files-status-warn";
      if (statusTimer) clearTimeout(statusTimer);
      statusTimer = setTimeout(clearStatus, 6000);
    }

    // The status × cancels an in-flight import; with nothing running it just
    // dismisses a finished notice. Aborting the uploads alone would only stop the
    // conversion once the server notices the dropped connection (seconds later),
    // so we also POST an explicit cancel that stops the conversion right away.
    if (statusClose) statusClose.addEventListener("click", function () {
      if (importAbort) {
        importAbort.abort();
        fetch(apiURL("api/note/import/cancel"), { method: "POST" }).catch(function () {});
      } else {
        clearStatus();
      }
    });
  }

  // Sort the tree A→Z or Z→A, client-side. The server always emits folders-first,
  // names A→Z; we reverse the file/folder order within each container when Z→A is
  // chosen, and re-apply after every tree re-render (toggled by the toolbar icon).
  function initFilesSort(tree) {
    var btn = getEl("g-files-sort");
    if (!btn) return;
    var desc = false, observer = null;
    function apply() {
      // Our appendChilds mutate the tree, which would re-fire this observer; pause
      // it across the sort so we don't loop.
      if (observer) observer.disconnect();
      sortContainer(tree);
      tree.querySelectorAll(".g-tree-children").forEach(sortContainer);
      if (observer) observer.observe(tree, { childList: true });
    }
    // Sort one container's children by name, folders always before files. Reads
    // each row's name from the row element (.g-tree-row carries data-name; the
    // folder's row is its <summary>). Sorting by key is idempotent, so re-running
    // on the tree's own mutations settles instead of flip-flopping.
    function rowName(el) {
      var r = el.matches(".g-tree-row") ? el : el.querySelector(".g-tree-row");
      return ((r && r.getAttribute("data-name")) || "").toLowerCase();
    }
    function sortContainer(container) {
      var kids = Array.prototype.slice.call(container.children);
      var sorted = kids.slice().sort(function (a, b) {
        var af = a.classList.contains("g-tree-folder"), bf = b.classList.contains("g-tree-folder");
        if (af !== bf) return af ? -1 : 1; // folders first, both directions.
        var cmp = rowName(a) < rowName(b) ? -1 : rowName(a) > rowName(b) ? 1 : 0;
        return desc ? -cmp : cmp;
      });
      // Only move rows that are actually out of place. Re-appending an open
      // <details> can fire a spurious toggle (folding it); skipping no-op reorders
      // (the common case: the server already emits A→Z) avoids that entirely.
      var same = kids.every(function (k, i) { return k === sorted[i]; });
      if (same) return;
      sorted.forEach(function (k) { container.appendChild(k); });
    }
    btn.addEventListener("click", function () {
      desc = !desc;
      btn.name = desc ? "sort-up-alt" : "sort-down-alt";
      btn.title = desc ? "Sort: Z → A" : "Sort: A → Z";
      apply();
    });
    observer = new MutationObserver(apply);
    observer.observe(tree, { childList: true });
  }

  // Replace a note or folder row's name with an inline rename input. Commit moves
  // the file/folder within its own parent (the server adds .md for notes); cancel
  // restores the tree from the server. A folder row carries data-folder, a note
  // data-note — the right rename trigger fires for each.
  function startRowRename(row) {
    var nameEl = row.querySelector(".g-tree-name");
    if (!nameEl || row.querySelector(".g-tree-edit")) return;
    var isFolder = row.classList.contains("g-tree-folder-row");
    var oldPath = row.getAttribute(isFolder ? "data-folder" : "data-note");
    var oldName = row.getAttribute("data-name") || "";
    var input = document.createElement("input");
    input.type = "text";
    input.className = "g-tree-edit";
    input.autocomplete = "off";
    input.value = oldName;
    input.spellcheck = false;
    nameEl.replaceWith(input);
    input.focus();
    input.select();
    // Bring the row into view so a note created below the fold is visible to
    // rename right away. "nearest" so it doesn't jump when the row is already shown.
    row.scrollIntoView({ block: "nearest" });
    // Clicks would open the note / toggle the folder; swallow them while renaming.
    input.addEventListener("click", function (e) { e.stopPropagation(); e.preventDefault(); });

    var done = false;
    function commit(save) {
      if (done) return;
      done = true;
      var next = input.value.trim();
      if (save && next !== "" && next !== oldName) {
        // Keep the item in its own parent folder, swap the last path segment.
        var dir = oldPath.lastIndexOf("/") >= 0 ? oldPath.slice(0, oldPath.lastIndexOf("/") + 1) : "";
        var newPath = dir + next;
        setSignal("gRenamePath", newPath);
        if (isFolder) {
          fireWithSignal("gFolderPath", oldPath, "g-folder-rename-trigger");
          if (nav) nav.renameNotesUnder(oldPath, newPath); // rebind open note tabs under it.
        } else {
          fireWithSignal("gNotePath", oldPath, "g-note-rename-trigger");
          // The server keeps .md/.markdown, else appends .md (ensureMarkdownExt).
          // Rebind the open tab to that path so it isn't stranded or duplicated.
          var ext = /\.(md|markdown)$/i.test(newPath) ? newPath : newPath + ".md";
          if (nav) nav.renameNote(oldPath, ext);
        }
      } else {
        getEl("g-files-trigger").click(); // restore the row from the server.
      }
    }
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); commit(true); }
      else if (e.key === "Escape") { e.preventDefault(); commit(false); }
    });
    input.addEventListener("blur", function () { commit(true); });
  }

  // openAncestors expands every folder enclosing a row, so a row created inside a
  // collapsed folder becomes visible (for its inline rename). It also records the
  // opened paths in the fold set so they survive the next tree re-render.
  function openAncestors(row, folds) {
    var d = row.closest("details.g-tree-folder");
    // For a folder row the closest details is its own; start above it.
    if (row.classList.contains("g-tree-folder-row") && d) d = d.parentElement.closest("details.g-tree-folder");
    while (d) {
      d.open = true;
      var path = d.querySelector(".g-tree-folder-row");
      if (path && folds) folds.add(path.getAttribute("data-folder"));
      d = d.parentElement.closest("details.g-tree-folder");
    }
  }

  // initFolderState makes folder fold state survive the full tree re-render that
  // every create/rename/delete triggers (the server re-emits folders collapsed).
  // It keeps a Set of open folder paths, updated whenever a folder is toggled, and
  // re-applies it after each re-render — keyed by path so it's robust to reorders.
  // Returns the set with .add/.clear helpers used by the create/collapse actions.
  function initFolderState(tree) {
    var open = Object.create(null); // path -> true.
    var filter = getEl("g-files-filter");
    function pathOf(details) {
      var row = details.querySelector(".g-tree-folder-row");
      return row ? row.getAttribute("data-folder") : null;
    }
    // Record open/close as the user toggles folders. <details> fires a
    // non-bubbling "toggle"; a capture listener on the tree still receives it.
    tree.addEventListener("toggle", function (e) {
      var d = e.target;
      if (!d.classList || !d.classList.contains("g-tree-folder")) return;
      var p = pathOf(d);
      if (!p) return;
      if (d.open) open[p] = true; else delete open[p];
    }, true);
    // Re-apply remembered open folders. The whole tree re-renders (collapsed) on
    // every create/rename/delete, which would otherwise fold open folders. Skipped
    // while filtering (the filter owns folds then). Re-asserted on the next frame
    // so it wins over any other observer that reorders/replaces rows in the batch.
    function restore() {
      if (filter && (filter.value || "").trim()) return;
      tree.querySelectorAll(".g-tree-folder").forEach(function (d) {
        var p = pathOf(d);
        if (p && open[p]) d.open = true;
      });
    }
    // Watch the whole subtree: Datastar morphs the tree in place, so a new row
    // appears deep inside an existing folder (no change to #g-files' own children)
    // and the morph strips the open attribute off folders it keeps — both of which
    // a shallow childList observer would miss.
    var obs = new MutationObserver(function () {
      obs.disconnect(); // restore() flips open back on, which would re-fire us.
      restore();
      obs.observe(tree, { childList: true, subtree: true });
      requestAnimationFrame(restore);
    });
    obs.observe(tree, { childList: true, subtree: true });
    return {
      add: function (p) { if (p) open[p] = true; },
      clear: function () { open = Object.create(null); },
    };
  }

  // cssEscape quotes a value for use in a CSS attribute selector (note paths can
  // contain spaces and other characters); falls back when CSS.escape is absent.
  function cssEscape(s) {
    if (window.CSS && CSS.escape) return CSS.escape(s);
    return s.replace(/["\\]/g, "\\$&");
  }

  // isVaultRelativeHref reports whether an href in rendered content points at a
  // file inside the vault (a relative path), as opposed to an external URL, a
  // data: URI, an in-page #anchor, or a link the dedicated handlers already own
  // (the note scheme, the vault-file route). Such links must be intercepted so
  // the webview never navigates to a path the server doesn't route (a 404).
  function isVaultRelativeHref(href) {
    if (!href) return false;
    if (href.charAt(0) === "#") return false; // in-page anchor.
    return !/^(?:[a-z][a-z0-9+.-]*:|\/\/|vault-file\/)/i.test(href);
  }

  // isNoteHref reports whether a vault-relative href targets a Markdown note
  // (which opens as a tab) rather than some other file (which opens externally).
  function isNoteHref(href) {
    return /\.(?:md|markdown)$/i.test(href.split(/[?#]/)[0]);
  }

  // showLinkNotice warns when a rendered link can't be opened — the target is
  // missing, a directory, or outside the vault. Used instead of navigating to a
  // dead link.
  function showLinkNotice(path) {
    window.massToast("Can't open " + path + " — it may be missing or not a file.",
      { variant: "warning" });
  }

  // Preview navigator: opens notes (from search-result links and [[wikilinks]])
  // and keeps a back/forward history. Position -1 is the chat view (preview
  // closed); positions 0..n-1 are notes. Going back from the first note returns
  // to the chat; a new note open truncates any forward entries.
  var NOTE_SCHEME = "grimoire-note:";
  // nav is the shared view-history navigator. Every view you open — a chat
  // session, a note (which overlays the conversation), or the empty prompt — is a
  // history entry, so Back/Forward (buttons, mouse, Escape) walk the full trail.
  // nav is the tabbed-workspace API (openNote/openSession/openGraph/focus*/close*/
  // home/focusedNotePath). initPreview owns it; the lists/links open things as tabs.
  var nav = null;
  var navRestore = null; // set by initPreview; called once all init*() are wired.
  // editorAPI lets the workspace reopen the body editor with cached unsaved text
  // when a note tab is refocused (best-effort). Set by initEditor.
  var editorAPI = null;
  // showGraph builds/redraws the similarity graph once its overlay is shown and
  // sized. render() (the single source of truth) calls it right after revealing the
  // graph overlay, so the load is driven by the actual show — not a poll that could
  // race ahead and fetch + draw into a still-hidden, zero-size panel. Set by initGraph.
  var showGraph = null;

  function initPreview() {
    var back = getEl("g-preview-back");
    var fwd = getEl("g-preview-fwd");
    var trigger = getEl("g-preview-trigger");
    var closeBtn = document.querySelector(".g-preview-close");
    var body = getEl("g-preview-body");
    var panel = getEl("g-preview");
    var section = getEl("g-preview-section"); // breadcrumb suffix in the title.
    var scrollGen = 0; // bumped per open so a stale poll loop bails out.
    var activeHeading = null; // the heading we jumped to, while still in view.

    // ── Tabbed workspace ──────────────────────────────────────────────────
    // The main panel is a set of open tabs (notes, sessions, the graph). Tabs are
    // lightweight references; only the FOCUSED tab renders into the shared preview/
    // conversation/graph nodes ("tabs index, one renders"), so every existing
    // server handler is reused unchanged. Per-tab scroll (and unsaved editor text)
    // is cached in JS so refocusing a tab feels lossless despite the re-render.
    var tabs = [];          // [{ id, kind:"note"|"session"|"graph", ref, title }]
    var focusedID = null;   // id of the focused tab, or null for the empty prompt.
    var tabCache = {};      // id -> { scrollTop, editorText?, editorDirty? }
    var tabSeq = 0;         // monotonic id source.

    // Show "› Section" in the title after a section jump; clear it once the
    // heading scrolls out of view (so the title never claims a section you left).
    function setSection(headEl) {
      activeHeading = headEl;
      if (section) section.textContent = headEl ? " › " + headEl.textContent.trim() : "";
    }
    if (body) {
      body.addEventListener("scroll", function () {
        if (!activeHeading) return;
        var top = body.getBoundingClientRect().top;
        var r = activeHeading.getBoundingClientRect();
        // Cleared once the heading rises above the viewport or is pushed well below.
        if (r.bottom < top || r.top > top + body.clientHeight) setSection(null);
      });
    }

    // clearActiveSession drops the active-session highlight in the sidebar. Used
    // when the focused tab isn't a session (note, graph, or empty), so the list
    // doesn't show a stale session as selected. (Focusing a session re-renders the
    // list server-side and re-marks the active one, so that direction is handled.)
    function clearActiveSession() {
      var sessions = getEl("g-sessions");
      if (sessions) sessions.querySelectorAll(".g-session-active").forEach(function (r) { r.classList.remove("g-session-active"); });
    }

    // hidePreview closes the note overlay without it being a user "close" (the ×
    // handler bails while suppressClose is set), so focusing a session/graph/empty
    // tab can hide the preview underneath without side effects.
    var suppressClose = false;
    function hidePreview() {
      if (panel && panel.style.display === "none") return;
      suppressClose = true;
      if (closeBtn) closeBtn.click(); // $gPreviewOpen = false.
      suppressClose = false;
    }
    // setGraph shows/hides the similarity-graph overlay by toggling a plain class —
    // synchronous and deterministic. (It used a Datastar data-show signal via hidden
    // buttons, but that build's reactive display churn left the overlay flickering
    // hidden on launch; a direct class is simpler and can't race.)
    function setGraph(on) {
      var graphEl = getEl("g-graph");
      if (graphEl) graphEl.classList.toggle("g-graph-open", on);
    }

    function findTab(id) { for (var i = 0; i < tabs.length; i++) if (tabs[i].id === id) return tabs[i]; return null; }
    function focusedTab() { return focusedID === null ? null : findTab(focusedID); }
    // The Files tree highlights the focused note (used by initActiveNote).
    function focusedNotePath() { var t = focusedTab(); return t && t.kind === "note" ? t.ref : ""; }

    // saveFocusedCache stashes the outgoing tab's scroll position and any unsaved
    // editor text, so refocusing restores them (best-effort).
    function saveFocusedCache() {
      var t = focusedTab();
      if (!t) return;
      var c = tabCache[t.id] || (tabCache[t.id] = {});
      if (t.kind === "note") {
        if (body) c.scrollTop = body.scrollTop;
        var editor = getEl("g-editor"), editorText = getEl("g-editor-text");
        if (editor && editor.classList.contains("g-editor-open") && editorText) {
          c.editorText = editorText.value;
          c.editorDirty = true;
        }
      } else if (t.kind === "session") {
        var stream = getEl("g-stream");
        if (stream) c.scrollTop = stream.scrollTop;
      }
    }

    // ── Vaults-tab graph view ──
    // While the Vaults sidebar tab is active the workspace IS that vault's
    // similarity graph: the tab strip is hidden and the graph overlay fills the
    // panel. It's a view MODE, not a tab — the open tabs, the focused one, their
    // scroll and their server-side persistence are untouched, so leaving Vaults
    // brings the workspace back exactly as it was. The sidebar group's active tab
    // is the source of truth: show() sets it synchronously, so a programmatic
    // switch reads right even before its sl-tab-show lands.
    var vaultGraph = false; // the applied mode; render() and the strip follow it.
    function vaultTabActive() {
      var group = getEl("g-tabs");
      if (group && group.activeTab) return group.activeTab.panel === "vaults";
      // The group hasn't upgraded/rendered yet (a fast reload can get here before
      // Shoelace's first update), so activeTab is unset. Predict where it will
      // land: the saved sidebar tab, else the first tab — Vaults.
      var saved = null;
      try { saved = sessionStorage.getItem(TAB_KEY); } catch (e) { /* opaque storage */ }
      return SIDEBAR_PANELS[saved] ? saved === "vaults" : true;
    }
    // syncVaultGraph re-applies the rule after a sidebar tab change. It only acts
    // when the mode actually flips, so Files↔Sessions stays what it has always
    // been: a sidebar-only move that never touches the main panel.
    function syncVaultGraph() {
      var on = vaultTabActive();
      if (on === vaultGraph) return;
      vaultGraph = on;
      var app = getEl("app-grimoire");
      if (app) app.classList.toggle("g-vault-graph", on);
      if (on) saveFocusedCache(); // keep the outgoing tab's scroll + unsaved text.
      render();
    }
    // showSidebar switches the sidebar tab and applies the rule in the same beat,
    // for the actions that need the workspace back (home, a search).
    function showSidebar(name) {
      var group = getEl("g-tabs");
      if (group && typeof group.show === "function") group.show(name);
      syncVaultGraph();
    }
    // showGraphView reveals the graph overlay over the panel — the Graph tab's
    // view and the Vaults-tab view are the same picture.
    function showGraphView() {
      hidePreview(); clearActiveSession(); setGraph(true);
      if (showGraph) showGraph(); // build/redraw once the overlay is shown + sized.
    }

    // render drives the shared panel for the focused tab. It is the SINGLE source
    // of truth for "what's shown + which sidebar row is lit", so highlight state
    // can't drift from the view. Reuses the existing show paths (no history).
    function render() {
      var t = focusedTab();
      // The Vaults tab's graph view outranks the focused tab; otherwise the
      // focused tab decides what the panel shows.
      if (vaultGraph || (t && t.kind === "graph")) { showGraphView(); return; }
      if (!t) {                                  // empty prompt (no tabs at all).
        setGraph(false); hidePreview(); clearActiveSession();
        var clear = getEl("g-session-clear-trigger"); if (clear) clear.click();
        return;
      }
      setGraph(false);
      if (t.kind === "session") {
        hidePreview();
        // A placeholder session tab (id 0) is the "new, unsaved" session: show the
        // empty prompt. The first search in it creates a real session server-side
        // and adoptActiveSession() rebinds this tab to the new id.
        if (!t.ref.id || String(t.ref.id) === "0") {
          clearActiveSession();
          var c0 = getEl("g-session-clear-trigger"); if (c0) c0.click();
          return;
        }
        setSignal("gOpenURL", t.ref.url);
        requestAnimationFrame(function () {
          getEl("g-session-open-trigger").click(); // SetActiveSession + lights the row.
          var c = tabCache[t.id];
          if (c && typeof c.scrollTop === "number") {
            var stream = getEl("g-stream");
            if (stream) requestAnimationFrame(function () { stream.scrollTop = c.scrollTop; });
          }
        });
        return;
      }
      // note
      if (!trigger) return;
      clearActiveSession();
      setSignal("gPreviewPath", t.ref);
      // A trashed note (its path lives under .trash/) previews read-only: hide the
      // edit + run-save affordances so it's a peek-before-restore, not an edit.
      var preview = getEl("g-preview");
      if (preview) preview.classList.toggle("g-preview-readonly", (t.ref || "").indexOf(".trash/") === 0);
      trigger.click(); // server fills #g-preview-body.
      var c = tabCache[t.id];
      scrollGen += 1;
      if (c && typeof c.scrollTop === "number" && !(t.pendingHeading)) {
        restoreScroll(c.scrollTop, scrollGen, 30);
      } else {
        scrollToHeading(t.pendingHeading || "", scrollGen, 30);
      }
      t.pendingHeading = null;
      // Best-effort: restore unsaved editor text after the body re-renders.
      if (c && c.editorDirty && editorAPI) {
        var txt = c.editorText;
        requestAnimationFrame(function () { editorAPI.restore(txt); });
      }
    }

    // restoreScroll waits for the (re)rendered visible body, then restores the
    // cached scroll position — the scroll mirror of scrollToHeading.
    function restoreScroll(top, gen, tries) {
      if (gen !== scrollGen) return;
      var visible = panel && panel.style.display !== "none";
      if (visible && body) { body.scrollTop = top; return; }
      if (tries > 0) requestAnimationFrame(function () { restoreScroll(top, gen, tries - 1); });
    }

    function focus(id) {
      // Refocusing the same id still re-renders the strip + persists: openPreview
      // rebinds the focused preview tab to a new item in place (same id, new
      // ref/title), so the strip label and saved state would otherwise go stale.
      if (id !== focusedID) saveFocusedCache();
      focusedID = id;
      render();
      renderStrip();
      saveTabs();
    }

    function close(id) {
      var i = tabIndex(id);
      if (i < 0) return;
      // Closing the focused tab: prefer the right neighbour, then the left.
      var fallback = tabs[i + 1] || tabs[i - 1] || null;
      closeMany([id], fallback ? fallback.id : null);
    }

    // closeMany removes a set of tabs in one pass (the strip's right-click menu
    // closes whole ranges). focusFallbackID is the survivor to focus if the
    // focused tab is among those closed; null (or a tab that didn't survive) lands
    // focus on whatever remains — e.g. the placeholder ensureNotEmpty spawns.
    function closeMany(ids, focusFallbackID) {
      if (!ids.length) return;
      var drop = {};
      for (var k = 0; k < ids.length; k++) drop[ids[k]] = true;
      var losingFocus = drop[focusedID];
      var closedNotes = [];
      tabs = tabs.filter(function (t) {
        if (!drop[t.id]) return true;
        if (t.kind === "note") closedNotes.push(t.ref);
        delete tabCache[t.id];
        return false;
      });
      ensureNotEmpty(); // there's always at least one tab (a blank session).
      if (losingFocus) {
        var keep = focusFallbackID !== null && findTab(focusFallbackID);
        focusedID = (keep || tabs[0]).id;
        render();
      }
      renderStrip();
      saveTabs();
      closeNoteKernels(closedNotes);
    }

    // closeNoteKernels tears down the kernel session of each closed note so a
    // running shell doesn't outlive its tab. They share one trigger, so fire them
    // one per frame to avoid clobbering the gClosePath signal mid-flight.
    function closeNoteKernels(paths) {
      paths.forEach(function (path, i) {
        if (!path) return;
        setTimeout(function () {
          fireWithSignal("gClosePath", path, "g-note-close-trigger");
        }, i * 16);
      });
    }

    // closeToLeft / closeToRight / closeOthers / closeAllTabs back the strip's
    // right-click menu. Each computes the id set relative to the right-clicked tab,
    // then defers to closeMany (which preserves the always-one-tab invariant). The
    // right-clicked tab survives all but Close-all, so it's the focus fallback.
    function tabIndex(id) {
      for (var i = 0; i < tabs.length; i++) if (tabs[i].id === id) return i;
      return -1;
    }
    function closeToLeft(id) {
      var at = tabIndex(id);
      if (at <= 0) return;
      closeMany(tabs.slice(0, at).map(tabID), id);
    }
    function closeToRight(id) {
      var at = tabIndex(id);
      if (at < 0 || at >= tabs.length - 1) return;
      closeMany(tabs.slice(at + 1).map(tabID), id);
    }
    function closeOthers(id) {
      if (tabIndex(id) < 0) return;
      closeMany(tabs.filter(function (t) { return t.id !== id; }).map(tabID), id);
    }
    function closeAllTabs() {
      closeMany(tabs.map(tabID), null);
    }
    function tabID(t) { return t.id; }

    // openTabMenu shows the tab strip's right-click menu at (x, y) for the tab
    // `id`. One menu exists at a time; opening replaces any open one, and it's
    // dismissed on outside-click, Escape, or scroll. Items that would close
    // nothing (no tabs on that side, or only this tab open) are disabled.
    var tabMenu = null;
    function closeTabMenu() {
      if (tabMenu) { tabMenu.remove(); tabMenu = null; }
    }
    function openTabMenu(id, x, y) {
      closeTabMenu();
      var at = tabIndex(id);
      if (at < 0) return;
      var items = [
        { label: "Close others", run: function () { closeOthers(id); }, enabled: tabs.length > 1 },
        { label: "Close to the left", run: function () { closeToLeft(id); }, enabled: at > 0 },
        { label: "Close to the right", run: function () { closeToRight(id); }, enabled: at < tabs.length - 1 },
        { label: "Close all", run: closeAllTabs, enabled: true },
      ];
      var menu = document.createElement("div");
      menu.className = "g-tab-menu";
      items.forEach(function (it) {
        var row = document.createElement("div");
        row.className = "g-tab-menu-item" + (it.enabled ? "" : " g-tab-menu-disabled");
        row.textContent = it.label;
        if (it.enabled) {
          row.addEventListener("click", function () { closeTabMenu(); it.run(); });
        }
        menu.appendChild(row);
      });
      var host = getEl("app-grimoire") || document.body;
      host.appendChild(menu);
      // Clamp to the viewport so a tab near the right/bottom edge stays on-screen.
      var r = menu.getBoundingClientRect();
      var vw = document.documentElement.clientWidth, vh = document.documentElement.clientHeight;
      menu.style.left = Math.min(x, vw - r.width - 4) + "px";
      menu.style.top = Math.min(y, vh - r.height - 4) + "px";
      tabMenu = menu;
    }
    document.addEventListener("pointerdown", function (e) {
      if (tabMenu && !e.target.closest(".g-tab-menu")) closeTabMenu();
    });
    document.addEventListener("keydown", function (e) {
      if (tabMenu && e.key === "Escape") { e.stopPropagation(); closeTabMenu(); }
    }, true);
    if (strip) strip.addEventListener("scroll", closeTabMenu);

    // ensureNotEmpty guarantees at least one tab exists: a blank "new session"
    // placeholder (session id 0) shown as the empty prompt. Keeps the tab strip
    // always present and gives the input bar a session to write into.
    function ensureNotEmpty() {
      if (tabs.length) return;
      tabs.push({ id: ++tabSeq, kind: "session", ref: { id: 0, url: "", title: "New session" }, title: "New session" });
    }

    // tabKey is a tab's dedup identity within its kind (note path / session id /
    // the lone "graph"). Scratch sessions (id 0) share no key — each is distinct.
    function tabKey(kind, ref) {
      return kind === "note" ? ref : kind === "session" ? String(ref.id) : "graph";
    }

    // open finds an existing tab matching (kind, ref-key) and focuses it, else
    // appends a new one. Graph is a singleton; real sessions and notes dedup by
    // their id/path. A scratch session (ref.id 0) never dedups — each one is a
    // distinct blank tab, so repeated "+" adds new tabs. Opened tabs are pinned
    // (permanent); openPreview is the provisional single-click path.
    function open(kind, ref, title) {
      var dedup = kind === "graph" || (kind === "note") || (kind === "session" && ref.id);
      if (!dedup) {
        var tab0 = { id: ++tabSeq, kind: kind, ref: ref, title: title };
        tabs.push(tab0);
        return tab0;
      }
      var key = tabKey(kind, ref);
      for (var i = 0; i < tabs.length; i++) {
        var t = tabs[i];
        if (t.kind !== kind) continue;
        if (tabKey(t.kind, t.ref) === key) { if (title) t.title = title; return t; }
      }
      var tab = { id: ++tabSeq, kind: kind, ref: ref, title: title };
      tabs.push(tab);
      return tab;
    }

    // openPreview is the single-click (provisional) open, IDE-style: the item
    // takes over the one reusable "preview" tab of its kind rather than spawning a
    // new tab, so clicking down a list doesn't pile up tabs. If the item is already
    // open in a pinned tab, that tab is focused as-is. Double-clicking the row or
    // the tab, editing the note, or dragging the tab pins the preview so the next
    // click opens a fresh one.
    function openPreview(kind, ref, title) {
      var key = tabKey(kind, ref);
      var preview = null;
      for (var i = 0; i < tabs.length; i++) {
        var t = tabs[i];
        if (t.kind !== kind) continue;
        if (tabKey(t.kind, t.ref) === key) { if (title) t.title = title; return t; } // already open.
        if (t.preview && !preview) preview = t;
      }
      if (preview) { // reuse the kind's preview tab: rebind it to this item.
        delete tabCache[preview.id];
        preview.ref = ref;
        preview.title = title;
        preview.pendingHeading = "";
        return preview;
      }
      var tab = { id: ++tabSeq, kind: kind, ref: ref, title: title, preview: true };
      tabs.push(tab);
      return tab;
    }

    // pin makes a tab permanent (no longer the reusable preview tab). Idempotent;
    // re-renders the strip so the italic provisional styling clears.
    function pin(id) {
      var t = findTab(id);
      if (!t || !t.preview) return;
      t.preview = false;
      renderStrip();
      saveTabs();
    }

    // ── Tab strip rendering ──
    var strip = getEl("g-tabstrip-tabs");
    function renderStrip() {
      if (!strip) return;
      strip.innerHTML = "";
      var activeEl = null;
      for (var i = 0; i < tabs.length; i++) {
        var t = tabs[i];
        var el = document.createElement("div");
        el.className = "g-tab" + (t.id === focusedID ? " g-tab-active" : "") + (t.preview ? " g-tab-preview" : "");
        el.setAttribute("data-tab", String(t.id));
        el.draggable = true; // re-orderable by dragging (see the strip drag handlers).
        var label = document.createElement("span");
        label.className = "g-tab-title";
        label.textContent = t.title || titleFor(t);
        el.appendChild(label);
        var x = document.createElement("span");
        x.className = "g-tab-close";
        x.setAttribute("data-close", String(t.id));
        x.textContent = "×";
        el.appendChild(x);
        strip.appendChild(el);
        if (t.id === focusedID) activeEl = el;
      }
      // Keep the focused tab in view when the strip overflows (opening a new tab
      // off the right edge, or focusing one that's scrolled away).
      if (activeEl) activeEl.scrollIntoView({ inline: "nearest", block: "nearest" });
    }
    function titleForNote(path) {
      var base = (path || "").split("/").pop();
      return base.replace(/\.[^.]+$/, "") || base; // drop extension.
    }
    function titleFor(t) {
      if (t.kind === "graph") return "Graph";
      if (t.kind === "session") return t.ref && t.ref.title ? t.ref.title : "Session";
      return titleForNote(t.ref || "");
    }
    if (strip) {
      // Click focuses a tab; the second click of a double-click pins a preview tab,
      // IDE-style (the same materialize gesture as double-clicking its list row). We
      // pin off e.detail rather than a dblclick listener because focus() rebuilds the
      // strip DOM on the first click, detaching the element a dblclick would need.
      strip.addEventListener("click", function (e) {
        var x = e.target.closest("[data-close]");
        if (x) { e.stopPropagation(); close(parseInt(x.getAttribute("data-close"), 10)); return; }
        var tabEl = e.target.closest("[data-tab]");
        if (!tabEl) return;
        var id = parseInt(tabEl.getAttribute("data-tab"), 10);
        if (e.detail > 1) pin(id);
        else focus(id);
      });
      // Middle-click closes a tab (editor convention).
      strip.addEventListener("auxclick", function (e) {
        if (e.button !== 1) return;
        var tabEl = e.target.closest("[data-tab]");
        if (tabEl) { e.preventDefault(); close(parseInt(tabEl.getAttribute("data-tab"), 10)); }
      });

      // Right-click a tab for a bulk-close menu (editor convention). The menu is a
      // plain floating element — same thin, client-owned approach as the strip
      // itself; left/right items disable when that side is empty.
      strip.addEventListener("contextmenu", function (e) {
        var tabEl = e.target.closest("[data-tab]");
        if (!tabEl) return;
        e.preventDefault();
        openTabMenu(parseInt(tabEl.getAttribute("data-tab"), 10), e.clientX, e.clientY);
      });

      // Vertical mouse wheel scrolls the overflowing strip horizontally (a plain
      // wheel doesn't drive overflow-x). Only act when the strip actually overflows
      // and the gesture is vertical, so trackpad horizontal scroll passes through.
      strip.addEventListener("wheel", function (e) {
        if (strip.scrollWidth <= strip.clientWidth) return;
        if (Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
        strip.scrollLeft += e.deltaY;
        e.preventDefault();
      }, { passive: false });

      // Drag to re-order tabs. dragstart records the dragged id; dragover marks the
      // tab the cursor is over with a left/right insertion edge (by its midpoint);
      // drop moves the dragged tab to that slot in the tabs array.
      var dragId = null;
      function clearDragMarks() {
        strip.querySelectorAll(".g-tab-drop-before,.g-tab-drop-after")
          .forEach(function (el) { el.classList.remove("g-tab-drop-before", "g-tab-drop-after"); });
      }
      strip.addEventListener("dragstart", function (e) {
        var tabEl = e.target.closest("[data-tab]");
        if (!tabEl) return;
        dragId = parseInt(tabEl.getAttribute("data-tab"), 10);
        tabEl.classList.add("g-tab-dragging");
        e.dataTransfer.effectAllowed = "move";
        // A drag type marks this as an internal tab move (distinct from the file
        // import dropzone, which keys off the OS "Files" type).
        try { e.dataTransfer.setData("application/x-grimoire-tab", String(dragId)); } catch (err) { /* IE quirk. */ }
      });
      strip.addEventListener("dragover", function (e) {
        if (dragId === null) return;
        var tabEl = e.target.closest("[data-tab]");
        if (!tabEl || parseInt(tabEl.getAttribute("data-tab"), 10) === dragId) { clearDragMarks(); return; }
        e.preventDefault(); // allow drop.
        e.dataTransfer.dropEffect = "move";
        var r = tabEl.getBoundingClientRect();
        var after = e.clientX > r.left + r.width / 2;
        clearDragMarks();
        tabEl.classList.add(after ? "g-tab-drop-after" : "g-tab-drop-before");
      });
      strip.addEventListener("drop", function (e) {
        if (dragId === null) return;
        var tabEl = e.target.closest("[data-tab]");
        if (!tabEl) { clearDragMarks(); return; }
        e.preventDefault();
        var overId = parseInt(tabEl.getAttribute("data-tab"), 10);
        var r = tabEl.getBoundingClientRect();
        var after = e.clientX > r.left + r.width / 2;
        reorderTab(dragId, overId, after);
        clearDragMarks();
      });
      strip.addEventListener("dragend", function () {
        dragId = null;
        clearDragMarks();
        strip.querySelectorAll(".g-tab-dragging").forEach(function (el) { el.classList.remove("g-tab-dragging"); });
      });
    }
    // reorderTab moves the dragged tab to just before/after the target tab.
    function reorderTab(id, overId, after) {
      var from = -1, to = -1;
      for (var i = 0; i < tabs.length; i++) {
        if (tabs[i].id === id) from = i;
        if (tabs[i].id === overId) to = i;
      }
      if (from < 0 || to < 0 || from === to) return;
      var moved = tabs.splice(from, 1)[0];
      moved.preview = false; // deliberately re-ordering a tab pins it (IDE-style).
      // Recompute the target index after removal, then insert before/after it.
      var insert = to;
      if (from < to) insert -= 1; // removal shifted everything past `from` left by one.
      if (after) insert += 1;
      tabs.splice(insert, 0, moved);
      renderStrip();
      saveTabs();
    }
    // A new tab is context-aware (browser new-tab convention): on the Files
    // sidebar tab it creates a new note (a real file, opened as a tab — Grimoire
    // notes are always files, like Obsidian); on the Vaults tab it adds a vault;
    // elsewhere it opens a blank session scratch tab that commits nothing until
    // you search. Shared by the strip's "+" and the Ctrl+N shortcut. On Vaults the
    // strip (and its "+") is hidden by the graph view, so only Ctrl+N reaches this
    // — it still adds a vault, the one "new" that tab has.
    function newTab() {
      var group = getEl("g-tabs");
      var active = group && group.activeTab ? group.activeTab.panel : "sessions";
      if (active === "files") { var nn = getEl("g-new-note"); if (nn) nn.click(); }
      else if (active === "vaults") { var av = getEl("g-vault-add"); if (av) av.click(); }
      else nav.openScratch();
    }
    var tabNew = getEl("g-tab-new");
    if (tabNew) tabNew.addEventListener("click", newTab);

    // ── Persistence (per-vault, server-side) ──
    // Open tabs, the focused tab, and per-tab scroll positions persist in the
    // vault's UI-state store (SQLite) via /api/ui-state/tabs, so reopening the
    // vault restores the workspace. The store is per-vault by construction (each
    // vault is its own instance), so no key namespacing is needed. Saves are
    // debounced to coalesce rapid focus/scroll churn; a flush runs on unload.
    var TABS_URL = "api/ui-state/tabs";
    var saveTimer = null;

    function buildTabsPayload() {
      saveFocusedCache(); // capture the focused tab's current scroll first.
      var slim = tabs.map(function (t) { return { id: t.id, kind: t.kind, ref: t.ref, title: t.title, preview: !!t.preview }; });
      var scroll = {};
      for (var id in tabCache) {
        if (tabCache[id] && typeof tabCache[id].scrollTop === "number") scroll[id] = tabCache[id].scrollTop;
      }
      return { tabs: slim, focusedID: focusedID, seq: tabSeq, scroll: scroll };
    }

    function flushTabs() {
      if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
      var body = JSON.stringify(buildTabsPayload());
      // Prefer sendBeacon on unload (fetch may be cancelled as the page tears
      // down); fall back to a keepalive fetch otherwise.
      if (navigator.sendBeacon) {
        navigator.sendBeacon(apiURL(TABS_URL), new Blob([body], { type: "application/json" }));
        return;
      }
      fetch(apiURL(TABS_URL), { method: "POST", headers: { "Content-Type": "application/json" }, body: body, keepalive: true })
        .catch(function () { /* best-effort. */ });
    }

    function saveTabs() {
      if (saveTimer) clearTimeout(saveTimer);
      saveTimer = setTimeout(function () {
        saveTimer = null;
        fetch(apiURL(TABS_URL), {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(buildTabsPayload()),
        }).catch(function () { /* best-effort. */ });
      }, 400);
    }
    window.addEventListener("pagehide", flushTabs);
    window.addEventListener("beforeunload", flushTabs);

    function restoreTabs() {
      return fetch(apiURL(TABS_URL)).then(function (r) { return r.json(); }).then(function (s) {
        if (s && Array.isArray(s.tabs) && s.tabs.length) {
          tabs = s.tabs;
          tabSeq = typeof s.seq === "number" ? s.seq : tabs.reduce(function (m, t) { return Math.max(m, t.id); }, 0);
          focusedID = s.focusedID != null && findTab(s.focusedID) ? s.focusedID : tabs[tabs.length - 1].id;
          // Seed per-tab scroll so render() restores each tab's position.
          if (s.scroll) {
            for (var id in s.scroll) {
              (tabCache[id] || (tabCache[id] = {})).scrollTop = s.scroll[id];
            }
          }
        } else {
          ensureNotEmpty(); // fresh vault: one blank session tab.
          focusedID = tabs[0].id;
        }
      }).catch(function () {
        ensureNotEmpty();
        focusedID = tabs[0].id;
      }).finally(function () {
        renderStrip();
        render();
      });
    }

    // home is the book icon: return to the start view (the blank search prompt on
    // the Sessions sidebar tab) WITHOUT closing the user's open tabs.
    // Focus an existing blank scratch tab if there is one, else open one.
    function home() {
      showSidebar("sessions");
      var blank = null;
      for (var i = 0; i < tabs.length; i++) {
        if (tabs[i].kind === "session" && !tabs[i].ref.id) { blank = tabs[i]; break; }
      }
      if (blank) focus(blank.id);
      else { var t = open("session", { id: 0, url: "", title: "New session" }, "New session"); focus(t.id); }
    }

    // Expose the workspace so the lists/links can open things as tabs. Method names
    // mirror the old nav so call sites barely change.
    nav = {
      // openNote / openSession are the single-click (provisional) opens: the item
      // takes over its kind's reusable preview tab. openNotePinned / openSessionPinned
      // are the double-click opens that commit a permanent tab.
      openNote: function (path, heading) {
        var t = openPreview("note", path, titleForNote(path));
        t.pendingHeading = heading || "";
        focus(t.id);
      },
      openNotePinned: function (path, heading) {
        var t = open("note", path, null);
        t.preview = false;
        t.pendingHeading = heading || "";
        focus(t.id);
      },
      openSession: function (id, url, title) {
        var t = openPreview("session", { id: id, url: url, title: title || "" }, title || "");
        focus(t.id);
      },
      openSessionPinned: function (id, url, title) {
        var t = open("session", { id: id, url: url, title: title || "" }, title || "");
        t.preview = false;
        focus(t.id);
      },
      // pinFocused commits the focused preview tab (e.g. when its note is edited),
      // so a later single-click elsewhere opens a fresh preview instead of evicting it.
      pinFocused: function () { if (focusedID !== null) pin(focusedID); },
      openGraph: function () { var t = open("graph", null, "Graph"); focus(t.id); },
      // openScratch opens a NEW blank session tab — the empty search prompt. It
      // commits nothing: searching in it creates the real session (then the tab
      // rebinds to it). Scratch sessions (id 0) don't dedup, so each call adds a fresh blank.
      openScratch: function () { var t = open("session", { id: 0, url: "", title: "New session" }, "New session"); focus(t.id); },
      closeSession: function (id) {
        for (var i = 0; i < tabs.length; i++) {
          if (tabs[i].kind === "session" && String(tabs[i].ref.id) === String(id)) { close(tabs[i].id); return; }
        }
      },
      closeNote: function (path) {
        for (var i = 0; i < tabs.length; i++) {
          if (tabs[i].kind === "note" && tabs[i].ref === path) { close(tabs[i].id); return; }
        }
      },
      closeNotesUnder: function (folder) {
        var prefix = folder.replace(/\/$/, "") + "/";
        tabs.filter(function (t) { return t.kind === "note" && t.ref.indexOf(prefix) === 0; })
          .forEach(function (t) { close(t.id); });
      },
      // renameNote rebinds an open note tab from oldPath to newPath (and retitles
      // it) so a rename/move doesn't strand the old tab or spawn a duplicate. If a
      // tab for newPath already exists, the old one is dropped onto it.
      renameNote: function (oldPath, newPath) {
        var moved = null, dup = null;
        for (var i = 0; i < tabs.length; i++) {
          if (tabs[i].kind !== "note") continue;
          if (tabs[i].ref === oldPath) moved = tabs[i];
          else if (tabs[i].ref === newPath) dup = tabs[i];
        }
        if (!moved) return;
        if (dup) { close(moved.id); focus(dup.id); return; }
        moved.ref = newPath;
        moved.title = newPath.split("/").pop().replace(/\.[^.]+$/, "");
        if (tabCache[moved.id]) tabCache[moved.id].editorDirty = false; // path changed; drop stale edit cache.
        renderStrip(); saveTabs();
      },
      // renameNotesUnder rebinds every open note tab under oldFolder to newFolder
      // (a folder rename/move). Paths are prefix-rewritten.
      renameNotesUnder: function (oldFolder, newFolder) {
        var op = oldFolder.replace(/\/$/, "") + "/", np = newFolder.replace(/\/$/, "") + "/";
        var changed = false;
        for (var i = 0; i < tabs.length; i++) {
          var t = tabs[i];
          if (t.kind === "note" && t.ref.indexOf(op) === 0) {
            t.ref = np + t.ref.slice(op.length);
            changed = true;
          }
        }
        if (changed) { renderStrip(); saveTabs(); }
      },
      focusPrev: function () { stepFocus(-1); },
      focusNext: function () { stepFocus(1); },
      closeFocused: function () {
        if (focusedID !== null) { close(focusedID); return true; }
        return false;
      },
      home: home,
      focusedNotePath: focusedNotePath,
      // syncVaultGraph: apply the "Vaults tab shows the graph" rule for whatever
      // sidebar tab is active now. Called on every sidebar tab change and once at
      // init for the restored tab.
      syncVaultGraph: syncVaultGraph,
      // ensureSessionFocused: before a search, surface the conversation base
      // panel by hiding any preview/graph overlay, so the streamed results are
      // visible even if a note or the graph tab was focused.
      ensureSessionFocused: function () {
        // The Vaults tab's graph view covers the conversation, so a search leaves
        // it: back to Sessions, and the workspace returns as the user left it.
        if (vaultGraph) showSidebar("sessions");
        var t = focusedTab();
        // Remember which tab the results belong to, so adoptActiveSession rebinds
        // THIS tab when the (late) session list re-render arrives — even if the
        // user switches tabs while the results stream. A non-session focused tab
        // means the results will open a fresh session tab on adopt (null target).
        pendingAdoptTabID = t && t.kind === "session" ? t.id : null;
        if (t && t.kind === "session") return;
        saveFocusedCache();
        setGraph(false); hidePreview();
      },
      // adoptActiveSession: after the server records the turn it re-renders the
      // session list with the active row marked; rebind the searching tab to that
      // session (or open one). The session list re-renders at the END of the
      // search SSE stream, so we wait for the row to appear via a MutationObserver
      // rather than a fixed-frame poll (which could give up before the search
      // finished and leave the tab as "New session").
      adoptActiveSession: function () { adoptActiveSession(); },
    };
    // pendingAdoptTabID is the tab that initiated the in-flight search, set in
    // ensureSessionFocused. The rebind targets THIS tab (not whatever is focused
    // when the stream finishes), so switching tabs mid-search doesn't misfile it.
    var pendingAdoptTabID = null;
    function adoptActiveSession() {
      var list = getEl("g-sessions");
      if (!list) return;
      var done = false;
      function tryAdopt() {
        var row = list.querySelector(".g-session-active");
        if (!row) return false;
        done = true;
        applyAdopt(row.getAttribute("data-id"), row.getAttribute("data-open-url"),
          row.getAttribute("data-title") || "");
        return true;
      }
      if (tryAdopt()) return; // already present (e.g. a follow-up in a saved session).
      var obs = new MutationObserver(function () { if (tryAdopt()) obs.disconnect(); });
      obs.observe(list, { childList: true, subtree: true });
      // Safety net: stop observing after a generous window even if the row never
      // shows (e.g. the search errored before renderSessions).
      setTimeout(function () { if (!done) obs.disconnect(); }, 60000);
    }
    function applyAdopt(id, url, title) {
      // Prefer the tab that searched; fall back to the focused tab if it's gone.
      var target = (pendingAdoptTabID !== null && findTab(pendingAdoptTabID)) || focusedTab();
      pendingAdoptTabID = null;
      // The target already is this session, OR it's the blank placeholder (id 0)
      // the just-recorded turn turned into a real session: rebind it in place so
      // we don't spawn a second tab.
      if (target && target.kind === "session" &&
          (String(target.ref.id) === String(id) || !target.ref.id || String(target.ref.id) === "0")) {
        target.ref.id = id; target.ref.url = url; target.ref.title = title; target.title = title;
        renderStrip(); saveTabs();
        return;
      }
      // Otherwise open/focus the session tab WITHOUT re-running the server open
      // (the turn already rendered into the conversation): set state directly.
      var t = open("session", { id: id, url: url, title: title }, title);
      if (t.id !== focusedID) { saveFocusedCache(); focusedID = t.id; }
      renderStrip(); saveTabs();
    }
    function stepFocus(delta) {
      if (!tabs.length) return;
      var idx = 0;
      for (var i = 0; i < tabs.length; i++) if (tabs[i].id === focusedID) { idx = i; break; }
      var n = (idx + delta + tabs.length) % tabs.length;
      focus(tabs[n].id);
    }

    // Scroll the note to the section a result came from, matched by heading text
    // (robust to whatever id-slug the renderer would produce). Several things
    // make this fiddly, all handled by polling for the right conditions:
    //   - the body is patched in asynchronously after the @post;
    //   - clicking another section of the SAME note yields identical HTML, so
    //     there's no DOM mutation to hook;
    //   - reopening from chat means the panel is briefly display:none, and
    //     scrolling a hidden (zero-height) element is a no-op that sticks at the
    //     old position — so we wait until the panel is actually visible.
    // A generation token cancels a still-running poll when a newer click starts.
    function scrollToHeading(heading, gen, tries) {
      if (gen !== scrollGen) return; // superseded by a newer open.
      var want = heading.trim().toLowerCase();
      var visible = panel && panel.style.display !== "none";
      if (want && body && visible) {
        var heads = body.querySelectorAll("h1,h2,h3,h4,h5,h6");
        for (var i = 0; i < heads.length; i++) {
          if (heads[i].textContent.trim().toLowerCase() === want) {
            heads[i].scrollIntoView({ block: "start" });
            setSection(heads[i]);
            return;
          }
        }
      } else if (!want && visible) {
        setSection(null);
        if (body) body.scrollTop = 0; // no heading: show the note from the top.
        return;
      }
      if (tries > 0) requestAnimationFrame(function () { scrollToHeading(heading, gen, tries - 1); });
    }

    // openVaultLink handles a relative link inside rendered content. A note opens
    // as a tab; any other vault file is opened in the OS default editor via the
    // server. A failure (missing file, or a directory) shows a brief notice
    // rather than navigating, so the app never lands on a 404.
    function openVaultLink(href) {
      var path = href.split(/[?#]/)[0]; // drop any fragment/query.
      if (isNoteHref(path)) { nav.openNote(path, ""); return; }
      fetch(apiURL("api/open-file"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: path }),
      }).then(function (res) {
        if (!res.ok) showLinkNotice(path);
      }).catch(function () { showLinkNotice(path); });
    }

    // A click on a source/note link or an in-vault wikilink opens its target in
    // the panel. Note links carry data-heading to scroll to a section. A
    // Ctrl/Shift+click on a tree row builds a multi-selection instead of opening.
    document.addEventListener("click", function (e) {
      var hit = e.target.closest("[data-note]");
      if (hit) {
        if ((e.ctrlKey || e.metaKey || e.shiftKey) && hit.classList.contains("g-tree-note")) return;
        // A search covers every vault, but the page speaks to one: a hit from
        // another vault navigates there with the note to open (see openPendingNote).
        var hitVault = hit.getAttribute("data-vault");
        if (hitVault && hitVault !== pageVault()) {
          location.assign("/?vault=" + encodeURIComponent(hitVault) +
            "&note=" + encodeURIComponent(hit.getAttribute("data-note")));
          return;
        }
        nav.openNote(hit.getAttribute("data-note"), hit.getAttribute("data-heading"));
        return;
      }
      var link = e.target.closest('a[href^="' + NOTE_SCHEME + '"]');
      if (link) {
        e.preventDefault();
        nav.openNote(decodeURIComponent(link.getAttribute("href").slice(NOTE_SCHEME.length)), "");
        return;
      }
      // A relative link inside rendered note/result content points at another
      // vault file. Without this, the webview would navigate to it and the
      // server (which only serves notes and vault assets) returns a bare 404,
      // blowing away the whole app. A .md/.markdown target opens as a note tab;
      // anything else (e.g. a linked .go source) opens in the OS default editor.
      var rel = e.target.closest(".g-preview-body a[href], #g-conversation a[href]");
      if (rel && isVaultRelativeHref(rel.getAttribute("href"))) {
        e.preventDefault();
        openVaultLink(decodeURIComponent(rel.getAttribute("href")));
      }
    });

    // The preview's back/forward arrows step between open tabs (muscle memory).
    if (back) back.addEventListener("click", function () { stepFocus(-1); });
    if (fwd) fwd.addEventListener("click", function () { stepFocus(1); });

    // Mouse back (button 3) / forward (button 4) focus the previous / next tab.
    window.addEventListener("mouseup", function (e) {
      if (e.button !== 3 && e.button !== 4) return;
      if (!tabs.length) return;
      e.preventDefault();
      stepFocus(e.button === 3 ? -1 : 1);
    });

    // The preview × closes the focused tab (a note tab). suppressClose guards the
    // programmatic hidePreview() so it isn't treated as a user close.
    if (closeBtn) {
      closeBtn.addEventListener("click", function () {
        if (suppressClose) return;
        if (focusedID !== null) close(focusedID);
      });
    }

    // Tab keyboard shortcuts (all well-known): Ctrl/Cmd+W closes the focused tab,
    // Ctrl+Tab / Ctrl+Shift+Tab cycle to the next / previous tab. Esc is left for
    // dismissing overlays (find bar, editor, dialogs) — not closing tabs, so a
    // stray Esc can't lose an open tab. Closing does NOT drop to the empty prompt
    // unless that was the last tab.
    document.addEventListener("keydown", function (e) {
      if (!(e.ctrlKey || e.metaKey)) return;
      if (e.key === "w" || e.key === "W") {
        if (focusedID === null) return; // already empty: nothing to close.
        e.preventDefault();
        close(focusedID); // focuses a neighbour, or empty.
      } else if (e.key === "n" || e.key === "N") {
        e.preventDefault();
        newTab(); // new note on the Files tab, else a new scratch session.
      } else if (e.key === "Tab") {
        if (!tabs.length) return;
        e.preventDefault();
        stepFocus(e.shiftKey ? -1 : 1);
      }
    });
    // Expose restore so init() can run it once every init*() has wired its
    // listeners (the graph overlay observer in particular).
    navRestore = restoreTabs;
  }

  // In-note find: a scoped Ctrl+F over the open note. The native find scans the
  // whole page (sidebar, hidden conversation behind the preview), so it reports
  // matches that aren't visible. This searches only #g-preview-body and paints
  // matches with the CSS Custom Highlight API — no DOM mutation, so a note
  // re-render doesn't have stray <mark>s to reconcile.
  function initFind() {
    var bar = getEl("g-find");
    var body = getEl("g-preview-body");
    var input = getEl("g-find-input");
    var count = getEl("g-find-count");
    var preview = getEl("g-preview");
    if (!bar || !body || !input || !preview || !window.CSS || !CSS.highlights) return;

    var matches = []; // Range per match, in document order.
    var current = -1;

    function previewOpen() {
      // Datastar drives #g-preview's display via data-show; "" / non-"none" = open.
      return preview.style.display !== "none";
    }
    function clearHighlights() {
      CSS.highlights.delete("g-find");
      CSS.highlights.delete("g-find-current");
      matches = [];
      current = -1;
      count.textContent = "";
    }
    // Collect ranges for every case-insensitive occurrence of q in the note.
    function findRanges(q) {
      var ranges = [];
      if (!q) return ranges;
      var needle = q.toLowerCase();
      var walker = document.createTreeWalker(body, NodeFilter.SHOW_TEXT, null);
      var node;
      while ((node = walker.nextNode())) {
        var text = node.nodeValue.toLowerCase();
        var from = 0, at;
        while ((at = text.indexOf(needle, from)) !== -1) {
          var r = document.createRange();
          r.setStart(node, at);
          r.setEnd(node, at + needle.length);
          ranges.push(r);
          from = at + needle.length;
        }
      }
      return ranges;
    }
    function paint() {
      var all = new Highlight();
      for (var i = 0; i < matches.length; i++) all.add(matches[i]);
      CSS.highlights.set("g-find", all);
      var cur = new Highlight();
      if (current >= 0) cur.add(matches[current]);
      CSS.highlights.set("g-find-current", cur);
    }
    function update() {
      matches = findRanges(input.value.trim());
      if (matches.length === 0) {
        current = -1;
        count.textContent = input.value.trim() ? "0/0" : "";
      } else {
        current = 0;
      }
      paint();
      showCurrent();
    }
    function showCurrent() {
      if (current < 0) {
        if (input.value.trim()) count.textContent = "0/0";
        return;
      }
      count.textContent = current + 1 + "/" + matches.length;
      var rect = matches[current].getBoundingClientRect();
      var box = body.getBoundingClientRect();
      if (rect.top < box.top || rect.bottom > box.bottom) {
        body.scrollTop += rect.top - box.top - box.height / 2;
      }
    }
    function step(delta) {
      if (matches.length === 0) return;
      current = (current + delta + matches.length) % matches.length;
      paint();
      showCurrent();
    }
    function open() {
      bar.classList.add("g-find-open");
      input.focus();
      input.select();
      if (input.value.trim()) update();
    }
    function close() {
      bar.classList.remove("g-find-open");
      clearHighlights();
    }

    input.addEventListener("input", update);
    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); step(e.shiftKey ? -1 : 1); }
      else if (e.key === "Escape") { e.preventDefault(); close(); }
    });
    getEl("g-find-next").addEventListener("click", function () { step(1); });
    getEl("g-find-prev").addEventListener("click", function () { step(-1); });
    getEl("g-find-close").addEventListener("click", close);

    // Intercept Ctrl/Cmd+F only while the preview is open; otherwise leave the
    // native find alone (there's no single note to scope to).
    window.addEventListener("keydown", function (e) {
      if ((e.ctrlKey || e.metaKey) && (e.key === "f" || e.key === "F")) {
        if (!previewOpen()) return;
        e.preventDefault();
        open();
      }
    });
    // Re-run against fresh content when a different note loads, and drop stale
    // highlights when the preview closes.
    new MutationObserver(function () {
      if (bar.classList.contains("g-find-open")) update();
    }).observe(body, { childList: true, subtree: true, characterData: true });
  }

  // Keep the latest turn in view as results stream and turns append.
  function initAutoScroll() {
    var stream = getEl("g-stream");
    var convo = getEl("g-conversation");
    if (!stream || !convo) return;
    var pinned = true;
    stream.addEventListener("scroll", function () {
      pinned = stream.scrollHeight - stream.scrollTop - stream.clientHeight < 80;
    });
    new MutationObserver(function () {
      if (pinned) stream.scrollTop = stream.scrollHeight;
    }).observe(convo, { childList: true, subtree: true, characterData: true });
  }

  // initTurnMenu lets a user remove a search request (and its results) from a
  // session's history by right-clicking its request bubble — straight to the
  // confirm dialog, like deleting a note. Delegated on the stable #g-stream so it
  // survives the conversation panel being replaced on open/delete; scoped to the
  // request bubble so right-clicking a result card does nothing.
  function initTurnMenu() {
    var stream = getEl("g-stream");
    if (!stream) return;
    stream.addEventListener("contextmenu", function (e) {
      var bubble = e.target.closest(".g-bubble-user");
      if (!bubble) return;
      var turn = bubble.closest(".g-turn");
      var id = turn && turn.getAttribute("data-turn-id");
      if (!id) return;
      e.preventDefault();
      // Collapse whitespace (queries can be multi-line) and cap the length so a
      // long request doesn't blow out the confirm dialog.
      var query = (bubble.textContent || "").replace(/\s+/g, " ").trim();
      if (query.length > 60) query = query.slice(0, 60).trim() + "…";
      if (!query) query = "this request";
      confirmDelete.ask("Remove request",
        'Remove "' + query + '" and its results from this session?',
        function () { fireWithSignal("gTurnID", id, "g-turn-delete-trigger"); });
    });
  }

  // Copy buttons are server-rendered (a .g-code-copy on every code block); see
  // wrapCodeBlocks. The webview only wires the clipboard, via one delegated click
  // handler — so buttons patched in later need no re-attachment.
  function initCopy() {
    function copyText(text, btn) {
      var done = function () { flash(btn); };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text, done); });
      } else {
        fallbackCopy(text, done);
      }
    }
    function fallbackCopy(text, done) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand("copy"); } catch (e) { /* best effort */ }
      document.body.removeChild(ta);
      done();
    }
    function flash(btn) {
      btn.name = "check-lg";
      btn.classList.add("g-copied");
      setTimeout(function () { btn.name = "copy"; btn.classList.remove("g-copied"); }, 1200);
    }

    document.addEventListener("click", function (e) {
      var code = e.target.closest(".g-code-copy");
      if (code) {
        e.stopPropagation();
        var pre = code.closest(".g-code-block").querySelector("pre");
        var inner = pre && pre.querySelector("code");
        copyText(((inner || pre).innerText || "").replace(/\n$/, ""), code);
      }
    });
  }

  // Runnable code blocks: a Run button on each language-tagged block (added
  // server-side by wrapCodeBlocks) executes the block through its kernel. Delegated
  // on document so it works for blocks patched in on each preview. The clicked
  // block's language, source and id go into signals, plus the open note's path
  // (the per-note kernel session key), then the run trigger posts to the server,
  // which streams output back into this block's #g-code-output-<id> panel.
  function initRun() {
    // Click a hidden trigger button on the next frame, after signals are set, so
    // Datastar posts them with the request.
    function fireRunTrigger(id) {
      requestAnimationFrame(function () {
        var t = getEl(id);
        if (t) t.click();
      });
    }
    // Set the signals that identify a block run (its source, index, and the open
    // note's path) from a .g-code-block element — shared by run, save, and delete.
    function sendBlockRunSignals(block) {
      var pre = block.querySelector("pre");
      var inner = pre && pre.querySelector("code");
      var src = ((inner || pre).innerText || "").replace(/\n$/, "");
      setSignal("gRunCode", src);
      setSignal("gRunBlock", block.getAttribute("data-g-block") || "0");
      setSignal("gNotePath", getSignal("gPreviewPath"));
    }

    document.addEventListener("click", function (e) {
      // Run all blocks above + this one (⏩): the server reads the note and runs
      // blocks 0..this into the shared session, so JS only sends the note + index.
      var above = e.target.closest(".g-code-run-above");
      if (above) {
        e.stopPropagation();
        var aBlock = above.closest(".g-code-block");
        setSignal("gRunBlock", aBlock.getAttribute("data-g-block") || "0");
        setSignal("gNotePath", getSignal("gPreviewPath"));
        requestAnimationFrame(function () {
          var t = getEl("g-runabove-trigger");
          if (t) t.click();
        });
        return;
      }
      // Run all (per note): run every runnable block top-to-bottom into the shared
      // session. Reuses the run-above path with the LAST runnable block as target —
      // "run all" is "run above (and including) the final block".
      var runAll = e.target.closest("#g-runall-btn");
      if (runAll) {
        e.stopPropagation();
        var pbody = getEl("g-preview-body");
        var runnable = pbody ? pbody.querySelectorAll(".g-code-block .g-code-run") : [];
        if (!runnable.length) return;
        var lastBlock = runnable[runnable.length - 1].closest(".g-code-block");
        setSignal("gRunBlock", lastBlock.getAttribute("data-g-block") || "0");
        setSignal("gNotePath", getSignal("gPreviewPath"));
        fireRunTrigger("g-runabove-trigger");
        return;
      }
      // Save all (per note): commit every unsaved run in the open note.
      var saveAll = e.target.closest("#g-runsaveall-btn");
      if (saveAll) {
        e.stopPropagation();
        setSignal("gNotePath", getSignal("gPreviewPath"));
        fireRunTrigger("g-runsaveall-trigger");
        return;
      }
      // Discard all (per note): drop every unsaved re-run, reverting each block to
      // its saved output. The server re-renders the preview from the saved results.
      var discardAll = e.target.closest("#g-rundiscardall-btn");
      if (discardAll) {
        e.stopPropagation();
        setSignal("gNotePath", getSignal("gPreviewPath"));
        fireRunTrigger("g-rundiscardall-trigger");
        return;
      }
      // Remove all (per note): drop every saved result in the open note.
      var delAll = e.target.closest("#g-rundeleteall-btn");
      if (delAll) {
        e.stopPropagation();
        setSignal("gNotePath", getSignal("gPreviewPath"));
        fireRunTrigger("g-rundeleteall-trigger");
        return;
      }
      // Save (per block): commit the block's shown output as its saved result.
      // The note path + the block's source (the same signals a run sends) key the
      // pending run the server holds; the block id targets the slot to clear.
      var saveBtn = e.target.closest(".g-run-save-btn");
      if (saveBtn) {
        e.stopPropagation();
        sendBlockRunSignals(saveBtn.closest(".g-code-block"));
        fireRunTrigger("g-runsave-trigger");
        return;
      }
      // Discard (per block): drop this block's unsaved re-run and revert the panel
      // to the previously-saved output (or clear it if nothing was saved).
      var discardBtn = e.target.closest(".g-run-discard-btn");
      if (discardBtn) {
        e.stopPropagation();
        sendBlockRunSignals(discardBtn.closest(".g-code-block"));
        fireRunTrigger("g-rundiscard-trigger");
        return;
      }
      // Remove (per block): drop this block's saved output and clear its panel.
      var delBtn = e.target.closest(".g-run-del-btn");
      if (delBtn) {
        e.stopPropagation();
        sendBlockRunSignals(delBtn.closest(".g-code-block"));
        fireRunTrigger("g-rundelete-trigger");
        return;
      }
      var btn = e.target.closest(".g-code-run");
      if (!btn) return;
      e.stopPropagation();
      var block = btn.closest(".g-code-block");
      var pre = block.querySelector("pre");
      var lang = pre ? (pre.getAttribute("data-lang") || "") : "";
      var inner = pre && pre.querySelector("code");
      var src = ((inner || pre).innerText || "").replace(/\n$/, "");
      var id = block.getAttribute("data-g-block") || "0";
      // Per-block {kernel=FAMILY}{version=VER} overrides, if the fence carried them.
      var kernel = block.getAttribute("data-g-kernel") || "";
      var version = block.getAttribute("data-g-version") || "";
      setSignal("gRunLang", lang);
      setSignal("gRunCode", src);
      setSignal("gRunBlock", id);
      setSignal("gRunKernel", kernel);
      setSignal("gRunVersion", version);
      setSignal("gNotePath", getSignal("gPreviewPath"));
      requestAnimationFrame(function () {
        var t = getEl("g-runblock-trigger");
        if (t) t.click();
      });
    });

    // Show the note's "Save all" button only while some block has unsaved output.
    // The save slots are patched in by the server as runs finish; an observer on
    // the preview body keeps the header button in sync without bespoke wiring.
    var preview = getEl("g-preview");
    var body = getEl("g-preview-body");
    function refreshRunState() {
      if (!preview || !body) return;
      // An unsaved block shows a per-block save floppy (.g-run-save-btn); if any
      // exists, reveal the note-level "Save all" button. Any output panel with a
      // body means the note has results, so reveal the note-level "Remove all". A
      // runnable block (.g-code-run) reveals the note-level "Run all".
      preview.classList.toggle("g-has-unsaved", !!body.querySelector(".g-run-save-btn"));
      preview.classList.toggle("g-has-results", !!body.querySelector(".g-run-del-btn"));
      preview.classList.toggle("g-has-runnable", !!body.querySelector(".g-code-run"));
    }
    if (body) {
      new MutationObserver(refreshRunState).observe(body, { childList: true, subtree: true });
      refreshRunState(); // sync once in case a note is already rendered at init.
    }
  }

  // Properties editor (Obsidian-style): the panel is always editable, every
  // change auto-saves. Rows are server-rendered; JS handles mutations (add/remove
  // chip, rename key, add/remove property) and a debounced silent save. A per-note
  // undo/redo stack guards against misclicks (auto-save means a stray × hits disk
  // immediately) — Ctrl+Z / Ctrl+Shift+Z (or Ctrl+Y) restore prior states. Listens
  // on document so it keeps working after the panel is patched in on each preview.
  var propsHistory = [];   // snapshots of [{key, values}], oldest→newest.
  var propsCursor = -1;    // index of the current state in propsHistory.

  function initProps() {
    document.addEventListener("click", function (e) {
      var add = e.target.closest(".g-props-add");
      if (add) { addBlankProp(); return; }
      var delRow = e.target.closest(".g-prop-del");
      if (delRow) { delRow.closest(".g-prop").remove(); commitChange(); return; }
      var delChip = e.target.closest(".g-chip-del");
      if (delChip) { delChip.closest(".g-chip").remove(); commitChange(); return; }
    });
    document.addEventListener("keydown", function (e) {
      // Ctrl/Cmd+Z and Ctrl/Cmd+Shift+Z (or Ctrl+Y) undo/redo property edits.
      // Defer to the browser's native text undo only while a field actually holds
      // uncommitted text — otherwise (e.g. focus still in an empty add-value input
      // after pressing Enter) our property-level undo should run.
      if ((e.ctrlKey || e.metaKey) && (e.key === "z" || e.key === "Z" || e.key === "y" || e.key === "Y")) {
        // In the body editor textarea, leave undo to the browser's native one.
        if (e.target.closest(".g-editor-text")) return;
        var field = e.target.closest(".g-prop-key,.g-prop-addval");
        if (field && field.value.trim() !== "") return; // native text undo.
        if (!getEl("g-props")) return;
        var redo = e.key === "y" || e.key === "Y" || e.shiftKey;
        e.preventDefault();
        if (redo) redoProps(); else undoProps();
        return;
      }
      // Enter in an add-value input commits the typed value as a chip.
      if (e.key === "Enter") {
        var addval = e.target.closest(".g-prop-addval");
        if (addval) { e.preventDefault(); commitValue(addval); }
      }
    });
    // Update a row's type icon live as its key is typed (tags → tag glyph, etc.).
    document.addEventListener("input", function (e) {
      var keyEl = e.target.closest && e.target.closest(".g-prop-key");
      if (!keyEl) return;
      var icon = keyEl.closest(".g-prop").querySelector(".g-prop-icon");
      if (icon) icon.name = propIcon(keyEl.value);
    });
    document.addEventListener("blur", function (e) {
      // Commit a pending typed value, and save key renames, on blur.
      var addval = e.target.closest && e.target.closest(".g-prop-addval");
      if (addval && addval.value.trim()) { commitValue(addval); return; }
      if (e.target.closest && e.target.closest(".g-prop-key")) commitChange();
    }, true);

    // Seed the undo baseline whenever a note's panel is (re)rendered into the
    // preview body, so the first Ctrl+Z returns to the note's on-disk state.
    var pbody = getEl("g-preview-body");
    if (pbody) {
      new MutationObserver(function () {
        if (getEl("g-props")) seedProps();
      }).observe(pbody, { childList: true });
    }
  }

  function commitValue(addval) {
    var v = addval.value.trim();
    if (!v) return;
    addval.parentNode.insertBefore(chip(v), addval);
    addval.value = "";
    commitChange();
  }

  // Append a blank property row (focus its key) for the user to fill in. Not a
  // history checkpoint yet — that happens once it gains a key/value.
  function addBlankProp() {
    var list = document.querySelector("#g-props .g-props-list");
    if (!list) return;
    list.appendChild(buildRow("", []));
    var key = list.lastChild.querySelector(".g-prop-key");
    if (key) key.focus();
  }

  // propIcon maps a property key to a Shoelace icon, mirroring the server's
  // propIcon so a manually-added property gets the same glyph as a rendered one.
  function propIcon(key) {
    switch ((key || "").toLowerCase()) {
      case "tags": return "tags";
      case "aliases": return "signpost-split";
      case "title": return "type";
      case "date": case "created": case "updated": case "modified": return "calendar";
      default: return "text-left";
    }
  }

  // buildRow renders one editable property row from a key and its values.
  function buildRow(key, values) {
    var row = document.createElement("div");
    row.className = "g-prop";
    row.setAttribute("data-key", key);
    row.innerHTML =
      '<sl-icon name="' + propIcon(key) + '" class="g-prop-icon"></sl-icon>' +
      '<input class="g-prop-key" placeholder="property" spellcheck="false" autocomplete="off">' +
      '<div class="g-prop-vals"><input class="g-prop-addval" placeholder="add value…" spellcheck="false" autocomplete="off"></div>' +
      '<sl-icon-button class="g-prop-del" name="x" title="Remove property"></sl-icon-button>';
    row.querySelector(".g-prop-key").value = key;
    var vals = row.querySelector(".g-prop-vals");
    var addval = vals.querySelector(".g-prop-addval");
    (values || []).forEach(function (v) { vals.insertBefore(chip(v), addval); });
    return row;
  }

  // A removable value chip carrying its text in data-val.
  function chip(v) {
    var c = document.createElement("span");
    c.className = "g-chip";
    c.setAttribute("data-val", v);
    var t = document.createElement("span");
    t.className = "g-chip-text";
    t.textContent = v;
    var del = document.createElement("button");
    del.type = "button";
    del.className = "g-chip-del";
    del.title = "Remove";
    del.textContent = "×";
    c.appendChild(t);
    c.appendChild(del);
    return c;
  }

  // Read the panel's current properties into [{key, values}]. Empty keys skipped.
  function readPropsNow() {
    var props = [];
    document.querySelectorAll("#g-props .g-prop").forEach(function (row) {
      var keyEl = row.querySelector(".g-prop-key");
      var key = keyEl ? keyEl.value.trim() : "";
      if (!key) return;
      var chips = row.querySelectorAll(".g-chip");
      var values = Array.prototype.map.call(chips, function (c) { return c.getAttribute("data-val"); });
      var pending = row.querySelector(".g-prop-addval");
      if (pending && pending.value.trim()) values.push(pending.value.trim());
      props.push({ key: key, values: values });
    });
    return props;
  }

  // Render the panel's rows from a snapshot (used by undo/redo).
  function renderProps(snapshot) {
    var list = document.querySelector("#g-props .g-props-list");
    if (!list) return;
    list.textContent = "";
    snapshot.forEach(function (p) { list.appendChild(buildRow(p.key, p.values)); });
  }

  // Record the current state as a new history checkpoint, then save. The baseline
  // is seeded when the panel renders (seedProps); if a change somehow arrives
  // before that, seed now so undo still has a starting point. The stack is capped
  // (each snapshot is a full property copy) so a long edit session stays bounded;
  // the oldest checkpoints drop off the front.
  var MAX_PROPS_HISTORY = 128;
  function commitChange() {
    if (propsCursor < 0) seedProps();
    var snap = readPropsNow();
    // Truncate any redo tail, then push the new state.
    propsHistory = propsHistory.slice(0, propsCursor + 1);
    propsHistory.push(snap);
    if (propsHistory.length > MAX_PROPS_HISTORY) propsHistory = propsHistory.slice(propsHistory.length - MAX_PROPS_HISTORY);
    propsCursor = propsHistory.length - 1;
    saveProps(snap);
  }

  function undoProps() {
    if (propsCursor <= 0) return;
    propsCursor -= 1;
    renderProps(propsHistory[propsCursor]);
    saveProps(propsHistory[propsCursor]);
  }
  function redoProps() {
    if (propsCursor >= propsHistory.length - 1) return;
    propsCursor += 1;
    renderProps(propsHistory[propsCursor]);
    saveProps(propsHistory[propsCursor]);
  }

  // Seed the history baseline when a note's panel is first shown, so the first
  // undo can return to its on-disk state. Called when the panel (re)renders.
  function seedProps() {
    propsHistory = [readPropsNow()];
    propsCursor = 0;
  }

  // Save the given properties, debounced so a burst of edits writes once.
  var propsTimer = null;
  function saveProps(props) {
    if (propsTimer) clearTimeout(propsTimer);
    propsTimer = setTimeout(function () {
      propsTimer = null;
      setSignal("gNotePath", getSignal("gPreviewPath"));
      setSignal("gProps", JSON.stringify(props));
      requestAnimationFrame(function () { getEl("g-props-save-trigger").click(); });
    }, 400);
  }

  function getSignal(name) {
    var el = document.querySelector('[data-bind="' + name + '"]');
    return el ? el.value : "";
  }

  // Similarity graph: a force-directed map of notes, where an edge means two
  // notes' embeddings are similar (the server's /api/graph derives this from the
  // note centroids). The whole simulation + render is a small self-contained
  // canvas loop — no graph library. Nodes repel each other, edges pull their
  // endpoints together (stronger for more-similar pairs), and a weak gravity
  // keeps the whole thing centered. Left-click a node to open that note;
  // right-drag one to rearrange; scroll to zoom; drag the background to pan.
  function initGraph() {
    var overlay = getEl("g-graph");
    var canvas = getEl("g-graph-canvas");
    var countEl = getEl("g-graph-count");
    var reload = getEl("g-graph-reload");
    if (!overlay || !canvas) return;
    var ctx = canvas.getContext("2d");

    var nodes = [], edges = [];
    var maxDegree = 1;                     // busiest node, for size normalization.
    var view = { x: 0, y: 0, scale: 1 };   // pan offset + zoom.
    var running = false, raf = 0, alpha = 0;
    var drag = null, panning = null, hover = null, clicking = null;

    // Title filter: filterMatch holds the ids of nodes whose title matches the
    // query — the only nodes kept bright while the rest dim. Neighbours are NOT
    // auto-lit; the user hovers a match to reveal its connections. Recomputed by
    // applyFilter when the query or graph changes; empty query ⇒ inactive.
    var filterTerm = "";   // the raw query; matching is order-independent AND.
    var filterMatch = {};
    function filterActive() { return filterTerm.trim() !== ""; }
    function applyFilter() {
      filterMatch = {};
      if (!filterActive()) return;
      for (var i = 0; i < nodes.length; i++) {
        var nd = nodes[i];
        if (matchesFilter(nd.title, filterTerm)) filterMatch[nd.id] = true;
      }
    }

    function css(name) {
      return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || "#888";
    }
    // parseHex turns a #rgb/#rrggbb accent into {r,g,b}, or null if it isn't a
    // plain hex (canvas fillStyle can't resolve color-mix()/var() strings, so the
    // graph must derive its edge shades numerically rather than pass them through).
    function parseHex(c) {
      var m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(c);
      if (!m) return null;
      var h = m[1];
      if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
      return { r: parseInt(h.slice(0, 2), 16), g: parseInt(h.slice(2, 4), 16), b: parseInt(h.slice(4, 6), 16) };
    }
    // rgba/mix derive the edge shades from the accent in JS: edges are the
    // accent at reduced alpha, the lit (hover) edge the accent pushed toward the
    // theme's text colour — toward white on dark themes, toward near-black on
    // light ones, so the highlight gains contrast on either base (a fixed
    // lighten washed it out on light themes).
    function rgba(c, a) { return "rgba(" + c.r + "," + c.g + "," + c.b + "," + a + ")"; }
    function mixTo(c, d, t) {
      return "rgb(" + Math.round(c.r + (d.r - c.r) * t) + "," + Math.round(c.g + (d.g - c.g) * t) + "," + Math.round(c.b + (d.b - c.b) * t) + ")";
    }
    // Colours pulled live so the graph follows the theme.
    var colors = {};
    function refreshColors() {
      var accent = css("--mass-accent");
      var c = parseHex(accent);
      var txt = parseHex(css("--mass-text"));
      colors.node = accent;
      colors.nodeText = css("--mass-text");
      colors.edge = c ? rgba(c, 0.55) : accent;    // edges: accent, softened.
      colors.edgeLit = c && txt ? mixTo(c, txt, 0.35) : accent; // hovered node + its links.
      colors.matchText = css("--mass-text");          // matched label: high contrast.
      colors.matchHalo = css("--mass-bg-base");        // outline behind it for legibility.
    }

    // params reads the current slider values straight off the DOM (these are
    // Datastar signals, not data-bind inputs, so we read the controls).
    function params() {
      var k = getEl("g-graph-k"), m = getEl("g-graph-minsim");
      return { k: k ? k.value : 6, minSim: m ? m.value : 0.5 };
    }
    // graphURL is the cache key for the current slider settings; the same settings
    // always map to the same key.
    function graphURL() {
      var p = params();
      return apiURL("api/graph?k=" + encodeURIComponent(p.k) + "&minSim=" + encodeURIComponent(p.minSim));
    }

    // Built layouts are cached by URL so returning to the graph is instant — the
    // server-side rebuild runs once per distinct setting, not on every show. A
    // slider change is a new key and fetches fresh.
    var built = {}; // url -> { nodes, edges } laid-out, ready to swap in.
    // loadGen guards against overlapping loads (a slider change mid-retry): each
    // load() bumps it, and a stale fetch/retry chain bails when it no longer matches.
    var loadGen = 0;
    function load(force) {
      var url = graphURL();
      if (!force && built[url]) { swapIn(built[url]); return; }
      // Show the spinner while fetching + laying out (the empty "no notes" message
      // is suppressed by .g-graph-loading so it can't flash mid-load). The store
      // opens asynchronously on a cold start (an embedding-dimension probe that's a
      // gateway round-trip), so /api/graph answers 503 until it's ready — keep the
      // spinner up and retry with backoff rather than render a false "no notes".
      overlay.classList.add("g-graph-loading");
      var gen = ++loadGen;
      var done = function () { if (gen === loadGen) overlay.classList.remove("g-graph-loading"); };
      var delay = 250; // backoff between retries, capped below.
      function attempt() {
        if (gen !== loadGen) return; // superseded by a newer load().
        fetch(url, { headers: { Accept: "application/json" } })
          .then(function (r) {
            if (r.status === 503) { setTimeout(attempt, delay); delay = Math.min(delay * 2, 2000); return null; }
            return r.ok ? r.json() : { nodes: [], edges: [] };
          })
          .then(function (g) {
            if (g === null || gen !== loadGen) return; // retrying, or superseded.
            ingest(g); built[url] = { nodes: nodes, edges: edges }; done();
          })
          .catch(function () { if (gen === loadGen) { ingest({ nodes: [], edges: [] }); done(); } });
      }
      attempt();
    }

    // swapIn restores a previously built-and-laid-out graph instantly: adopt its
    // nodes/edges, refresh scale + counts, fit, and park.
    function swapIn(g) {
      nodes = g.nodes; edges = g.edges;
      present();
      alpha = 0;
      fitToContent();
      start();
    }

    // ingest seeds node positions on a circle (deterministic, so the layout is
    // stable across reloads), wires edges to node objects, and (re)starts the sim.
    function ingest(g) {
      var byId = {};
      var n = (g.nodes || []).length;
      nodes = (g.nodes || []).map(function (d, i) {
        var ang = (i / Math.max(1, n)) * Math.PI * 2;
        var nd = {
          id: d.id, title: d.title, degree: d.degree || 0,
          x: Math.cos(ang) * 240, y: Math.sin(ang) * 240, vx: 0, vy: 0,
        };
        byId[d.id] = nd;
        return nd;
      });
      edges = (g.edges || []).map(function (e) {
        return { a: byId[e.source], b: byId[e.target], w: e.weight };
      }).filter(function (e) { return e.a && e.b; });

      present();
      // Pre-settle the layout headlessly so it opens already stable — no visible
      // thrash, no perpetual drift. We run the full cooling schedule (alpha 1→0)
      // with no drawing, then open with alpha=0: the layout is final and the loop
      // parks immediately; interaction re-heats it. A fixed, generous iteration
      // budget converges these vault sizes (hundreds of nodes) well within a frame.
      view = { x: 0, y: 0, scale: 1 };
      if (nodes.length) {
        alpha = 1;
        for (var s = 0; s < 1000 && alpha > 0; s++) tick();
        alpha = 0;
      }
      fitToContent();
      start();
    }

    // present refreshes the size-normalization scale and the header count for the
    // current nodes/edges, and flags the empty state. Shared by a fresh ingest and
    // a cached swap-in.
    function present() {
      maxDegree = 1;
      for (var k = 0; k < nodes.length; k++) {
        nodes[k].adj = {}; // neighbour-id set, for hover highlighting.
        if (nodes[k].degree > maxDegree) maxDegree = nodes[k].degree;
      }
      for (var m = 0; m < edges.length; m++) {
        edges[m].a.adj[edges[m].b.id] = true;
        edges[m].b.adj[edges[m].a.id] = true;
      }
      overlay.classList.toggle("g-graph-blank", nodes.length === 0);
      if (countEl) countEl.textContent = nodes.length ? (nodes.length + " notes · " + edges.length + " links") : "";
      applyFilter(); // adj is now built; refresh the filter sets against it.
    }

    // fitToContent centers and zooms the view so the settled layout fills the
    // canvas with a margin, regardless of how far the simulation spread it.
    function fitToContent() {
      if (!nodes.length) return;
      var minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
      for (var i = 0; i < nodes.length; i++) {
        var nd = nodes[i];
        if (nd.x < minX) minX = nd.x;
        if (nd.x > maxX) maxX = nd.x;
        if (nd.y < minY) minY = nd.y;
        if (nd.y > maxY) maxY = nd.y;
      }
      resize();
      var w = maxX - minX || 1, h = maxY - minY || 1;
      var scale = Math.min((cssW - 80) / w, (cssH - 80) / h);
      view.scale = Math.max(0.15, Math.min(2, scale));
      view.x = -((minX + maxX) / 2) * view.scale;
      view.y = -((minY + maxY) / 2) * view.scale;
    }

    // One physics step, following d3-force's model (the same forces Obsidian's
    // graph uses), which handles mixed-degree graphs — hubs and lone leaves alike —
    // far better than hand-tuned springs. Three forces, all scaled by alpha:
    //   - charge (many-body): every pair repels with a 1/d² falloff and NO hard
    //     cutoff, so a stray leaf always feels a gentle push back toward the mass
    //     instead of escaping on a long edge. A minimum distance avoids blowups.
    //   - link: each edge moves its endpoints toward a target distance, biased by
    //     relative degree so the lower-degree end (a leaf) moves most — that snugs
    //     a leaf right up against its hub instead of dangling.
    //   - center: re-centres the whole layout on the origin by translating it (not
    //     by pulling each node in), so it can't fight cluster separation.
    var CHARGE = -800, LINK_DIST = 55, MIN_D2 = 25;
    function tick() {
      var i, j, a, b, dx, dy, d2, d, f;
      // Charge: applied as a velocity delta ~ charge*alpha/d² along the pair axis.
      for (i = 0; i < nodes.length; i++) {
        a = nodes[i];
        for (j = i + 1; j < nodes.length; j++) {
          b = nodes[j];
          dx = b.x - a.x; dy = b.y - a.y;
          d2 = dx * dx + dy * dy;
          if (d2 < MIN_D2) d2 = MIN_D2;
          f = CHARGE * alpha / d2;       // negative → repulsive.
          a.vx += dx * f; a.vy += dy * f;
          b.vx -= dx * f; b.vy -= dy * f;
        }
      }
      // Link: pull each edge toward LINK_DIST, degree-biased so the leaf end moves.
      for (i = 0; i < edges.length; i++) {
        var e = edges[i]; a = e.a; b = e.b;
        dx = b.x - a.x; dy = b.y - a.y;
        d = Math.sqrt(dx * dx + dy * dy) || 1;
        f = (d - LINK_DIST) / d * alpha * 0.5 * (0.5 + 0.5 * e.w);
        dx *= f; dy *= f;
        var ka = e.b.degree / (e.a.degree + e.b.degree); // a's share moves with b's degree.
        var kb = 1 - ka;
        if (a !== (drag && drag.node)) { a.x += dx * ka; a.y += dy * ka; }
        if (b !== (drag && drag.node)) { b.x -= dx * kb; b.y -= dy * kb; }
      }
      // Integrate velocity (charge) with damping; pin a dragged node.
      for (i = 0; i < nodes.length; i++) {
        a = nodes[i];
        if (a !== (drag && drag.node)) {
          a.vx *= 0.6; a.vy *= 0.6;     // velocity decay (d3 default-ish).
          a.x += a.vx; a.y += a.vy;
        } else { a.vx = 0; a.vy = 0; }
      }
      // Collision: push any overlapping pair apart so their circles (radius +
      // padding) never intersect, splitting the correction between them. Keeps
      // big hubs from swallowing their neighbours.
      var COLLIDE_PAD = 3;
      for (i = 0; i < nodes.length; i++) {
        a = nodes[i];
        var ra = radiusOf(a) + COLLIDE_PAD;
        for (j = i + 1; j < nodes.length; j++) {
          b = nodes[j];
          var rb = radiusOf(b) + COLLIDE_PAD, min = ra + rb;
          dx = b.x - a.x; dy = b.y - a.y;
          d2 = dx * dx + dy * dy;
          if (d2 >= min * min || d2 === 0) continue;
          d = Math.sqrt(d2) || 1;
          var push = (min - d) / d * 0.5; // half the overlap to each node.
          var px = dx * push, py = dy * push;
          if (a !== (drag && drag.node)) { a.x -= px; a.y -= py; }
          if (b !== (drag && drag.node)) { b.x += px; b.y += py; }
        }
      }
      // Center: translate the whole layout so its centroid sits at the origin.
      var cx = 0, cy = 0;
      for (i = 0; i < nodes.length; i++) { cx += nodes[i].x; cy += nodes[i].y; }
      cx /= nodes.length; cy /= nodes.length;
      for (i = 0; i < nodes.length; i++) { nodes[i].x -= cx; nodes[i].y -= cy; }
      alpha *= 0.992;
      if (alpha < 0.005) alpha = 0; // fully settle; interaction re-heats (reheat()).
    }

    function draw() {
      ctx.setTransform(1, 0, 0, 1, 0, 0);          // clear in device pixels.
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      ctx.save();
      ctx.scale(dpr, dpr);                          // map logical (CSS) px → device px.
      ctx.translate(cssW / 2 + view.x, cssH / 2 + view.y);
      ctx.scale(view.scale, view.scale);

      // Focus state, both narrowing what stays bright while the rest dims: a HOVER
      // focuses a node + its neighbours; a FILTER lights ONLY the matched nodes
      // (hover one to reveal its connections). Either active ⇒ focused.
      var focused = !!hover || filterActive();
      var lit = hover ? hover.adj : null;
      function isLit(nd) {
        if (!focused) return true;
        if (hover && (nd === hover || (lit && lit[nd.id]))) return true;
        return filterMatch[nd.id] === true;
      }
      function edgeLit(e) {
        if (!focused) return true;
        if (hover) return e.a === hover || e.b === hover;
        // Filter: light an edge only between two matches (rare); otherwise the
        // mesh dims so the matched nodes stand alone until hovered.
        return filterMatch[e.a.id] === true && filterMatch[e.b.id] === true;
      }

      // Edges first, under the nodes — thin and translucent so the node structure
      // reads through the mesh; weaker edges fade further toward invisible. Under a
      // hover, an incident edge brightens and the rest fade well back. A filter
      // (without a hover) skips edges entirely: the matched nodes stand alone for a
      // clean "here are your matches" view; hovering a match reveals its links.
      var i, e;
      if (!(filterActive() && !hover)) {
        for (i = 0; i < edges.length; i++) {
          e = edges[i];
          var base = 0.06 + 0.28 * Math.max(0, Math.min(1, e.w));
          var elit = edgeLit(e);
          ctx.strokeStyle = (focused && elit) ? colors.edgeLit : colors.edge;
          ctx.globalAlpha = focused ? (elit ? 0.85 : 0.03) : base;
          ctx.lineWidth = ((focused && elit ? 1.2 : 0.3) + 0.9 * e.w) / view.scale;
          ctx.beginPath();
          ctx.moveTo(e.a.x, e.a.y);
          ctx.lineTo(e.b.x, e.b.y);
          ctx.stroke();
        }
        ctx.globalAlpha = 1;
      }

      // Nodes: radius grows with degree (importance), normalized to this graph's
      // busiest node so the spread of sizes is visible even when most notes are
      // well-connected. sqrt keeps the area roughly proportional to degree and
      // stops hubs from ballooning. Labels dim with the node's relative size so
      // the dense middle reads as structure, not a wall of text.
      for (i = 0; i < nodes.length; i++) {
        var nd = nodes[i];
        var r = radiusOf(nd);                        // world-units; scales with zoom.
        var litNode = isLit(nd);
        var dim = focused && !litNode;
        ctx.globalAlpha = dim ? 0.12 : 1;
        ctx.beginPath();
        ctx.arc(nd.x, nd.y, r, 0, Math.PI * 2);
        // The hovered node, or a direct title match, paints in the accent colour.
        var isMatch = hover ? nd === hover : (filterActive() && filterMatch[nd.id] === true);
        ctx.fillStyle = isMatch ? colors.edgeLit : colors.node;
        ctx.fill();
      }
      ctx.globalAlpha = 1;

      // Labels last, in their own pass so text is never painted over by a later
      // node's dot or an edge. Visibility scales with importance: a big node reveals
      // its title at a low zoom, a small one only once zoomed in. Under focus:
      //   - hover: the hovered node + its neighbours always label bright (dimmed
      //     nodes keep faint labels);
      //   - filter: ONLY the matched node's title is force-shown (accent+bold); a
      //     lit neighbour follows the normal zoom rule, just dimmed like the rest.
      for (i = 0; i < nodes.length; i++) {
        var lnd = nodes[i];
        var lt = importanceOf(lnd);                  // 0..1 importance.
        var llit = isLit(lnd);
        var lmatch = hover ? lnd === hover : (filterActive() && filterMatch[lnd.id] === true);
        var lthreshold = 0.9 - 0.85 * lt;
        var lzoom = view.scale > lthreshold;
        var lnormal = lzoom ? Math.min(1, (view.scale - lthreshold) * 3) : 0;
        // A hovered node's lit neighbours read as *secondary* to the hovered node
        // itself: dimmer label and no bold/halo, so the hovered title stands out.
        var neighbour = hover && llit && lnd !== hover;
        var labelAlpha;
        if (lmatch) labelAlpha = 1;                  // matched (or hovered) node.
        else if (hover) labelAlpha = llit ? 0.75 : (lzoom ? 0.12 : 0);
        else if (filterActive()) labelAlpha = lnormal * 0.25; // neighbour/other: faint, not bright.
        else labelAlpha = lnormal;
        if (labelAlpha <= 0) continue;
        ctx.globalAlpha = labelAlpha;
        ctx.textAlign = "center";
        var lr = radiusOf(lnd);
        // Neighbours label *below* their dot so they sit a touch lower than the
        // hovered node's own title (drawn above its dot). Everything else labels
        // above by a fixed gap that's independent of the (large, hub) radius so it
        // never overlaps the dot.
        var gap = lr + 4 / view.scale;
        var lx = lnd.x, ly = neighbour ? lnd.y + 2 / view.scale : lnd.y - gap;
        // The hovered/matched node draws bold with a dark halo (stroke) so it stays
        // legible over the bright dots and edges. Neighbours and faint labels stay
        // plain so the hovered title clearly leads.
        var prominent = lmatch;
        if (prominent) {
          ctx.font = "bold " + (12 / view.scale) + "px sans-serif";
          ctx.lineWidth = 3 / view.scale;
          ctx.lineJoin = "round";
          ctx.strokeStyle = colors.matchHalo;
          ctx.strokeText(lnd.title, lx, ly);
          ctx.fillStyle = colors.matchText;
          ctx.fillText(lnd.title, lx, ly);
        } else {
          ctx.font = (11 / view.scale) + "px sans-serif";
          ctx.fillStyle = colors.nodeText;
          ctx.fillText(lnd.title, lx, ly);
        }
      }
      ctx.globalAlpha = 1;
      ctx.restore();
    }

    // The loop ticks physics only while there's energy (alpha) or an active
    // interaction, and parks itself once the layout is still — so a settled graph
    // doesn't drift forever or burn frames. Pan/zoom/drag call reheat() to wake it
    // for a redraw (interacting=true) without necessarily re-running the physics.
    var interacting = false;
    function frame() {
      if (!running) return;
      if (alpha > 0) tick();
      draw();
      if (alpha > 0 || interacting) {
        raf = requestAnimationFrame(frame);
      } else {
        running = false; // settled and idle: stop until the next interaction.
      }
    }
    function start() {
      resize();
      refreshColors();
      if (!running) { running = true; raf = requestAnimationFrame(frame); }
    }
    function stop() {
      running = false;
      if (raf) cancelAnimationFrame(raf);
    }
    // reheat wakes the loop after an interaction. heat>0 re-energizes the physics
    // (e.g. a drag that should let neighbours re-settle); heat=0 just forces a
    // redraw (pan/zoom, which move the view but not the layout).
    function reheat(heat) {
      if (heat) alpha = Math.max(alpha, heat);
      if (!running) { running = true; raf = requestAnimationFrame(frame); }
    }

    // resize sizes the canvas backing store to its CSS box × the device pixel
    // ratio, so rendering is crisp on high-DPI / scaled displays (otherwise the
    // browser stretches a CSS-pixel buffer and everything blurs, badly at large
    // window sizes). cssW/cssH hold the logical size the rest of the code works in
    // (centering, hit-testing); draw() pre-scales the context by dpr so those
    // logical coordinates map onto the larger backing store.
    var cssW = 1, cssH = 1, dpr = 1;
    function resize() {
      var rect = canvas.getBoundingClientRect();
      dpr = window.devicePixelRatio || 1;
      cssW = Math.max(1, Math.floor(rect.width));
      cssH = Math.max(1, Math.floor(rect.height));
      canvas.width = Math.floor(cssW * dpr);
      canvas.height = Math.floor(cssH * dpr);
    }

    // Map a pointer event to world coordinates (inverse of the draw transform).
    function toWorld(ev) {
      var rect = canvas.getBoundingClientRect();
      var px = ev.clientX - rect.left, py = ev.clientY - rect.top;
      return {
        x: (px - cssW / 2 - view.x) / view.scale,
        y: (py - cssH / 2 - view.y) / view.scale,
      };
    }
    // importanceOf maps a node's degree to 0..1 relative to the busiest node. The
    // exponent shapes the spread: ~1 is linear (bold size differences), <1
    // compresses. 0.8 keeps leaves visible while letting hubs stand out clearly.
    function importanceOf(nd) {
      return Math.pow(nd.degree / maxDegree, 0.8);
    }
    // radiusOf is the node's world-unit radius (same curve draw() uses), so the
    // hit test and the rendering can't drift apart. A wide min→max range makes the
    // importance of a node read at a glance.
    function radiusOf(nd) {
      return 3 + 22 * importanceOf(nd);
    }
    function nodeAt(wx, wy) {
      // Add a few screen pixels of slack (back into world units) so small nodes
      // stay easy to hit when zoomed out.
      for (var i = nodes.length - 1; i >= 0; i--) {
        var nd = nodes[i];
        var r = radiusOf(nd) + 4 / view.scale;
        if ((nd.x - wx) * (nd.x - wx) + (nd.y - wy) * (nd.y - wy) <= r * r) return nd;
      }
      return null;
    }

    // openNode opens a node's note as a permanent (pinned) tab — a deliberate
    // navigation from the graph, not a list-style preview. render() hides the graph
    // overlay for the note entry, so no explicit close is needed here.
    function openNode(nd) {
      if (nav) nav.openNotePinned(nd.id, "");
    }

    // Left-click a node to open its note; right-drag a node to move it. Either
    // button on the background pans. A left press on a node only opens if the
    // pointer stays put (CLICK_SLOP), so a left drag across the canvas pans past
    // nodes instead of opening one on release.
    var CLICK_SLOP = 4; // px
    canvas.addEventListener("pointerdown", function (ev) {
      if (ev.button !== 0 && ev.button !== 2) return;
      canvas.setPointerCapture(ev.pointerId);
      interacting = true;
      var w = toWorld(ev), hit = nodeAt(w.x, w.y);
      if (hit && ev.button === 2) { drag = { node: hit }; reheat(0.2); } // let neighbours re-settle.
      else {
        if (hit) clicking = { node: hit, x: ev.clientX, y: ev.clientY };
        panning = { x: ev.clientX, y: ev.clientY };
        reheat(0);
      }
    });
    // Right-drag is the node grab, so the browser menu never gets a look in.
    canvas.addEventListener("contextmenu", function (ev) { ev.preventDefault(); });
    canvas.addEventListener("pointermove", function (ev) {
      if (drag) {
        var w = toWorld(ev);
        drag.node.x = w.x; drag.node.y = w.y; drag.node.vx = 0; drag.node.vy = 0;
        reheat(0.1);
      } else if (panning) {
        if (clicking &&
          (Math.abs(ev.clientX - clicking.x) > CLICK_SLOP || Math.abs(ev.clientY - clicking.y) > CLICK_SLOP)) {
          clicking = null; // moved: this is a pan, not a click on the node.
        }
        view.x += ev.clientX - panning.x; view.y += ev.clientY - panning.y;
        panning.x = ev.clientX; panning.y = ev.clientY;
        reheat(0);
      } else {
        // Hover focus: highlight the node under the cursor and its connections.
        var w2 = toWorld(ev), h = nodeAt(w2.x, w2.y);
        if (h !== hover) { hover = h; canvas.style.cursor = h ? "pointer" : ""; reheat(0); }
      }
    });
    canvas.addEventListener("pointerleave", function () {
      if (hover) { hover = null; reheat(0); }
    });
    canvas.addEventListener("pointerup", function () {
      var open = clicking;
      drag = null; panning = null; clicking = null;
      interacting = false; // let the loop park once it settles.
      if (open) openNode(open.node);
    });
    canvas.addEventListener("wheel", function (ev) {
      ev.preventDefault();
      var factor = ev.deltaY < 0 ? 1.1 : 0.9;
      view.scale = Math.max(0.15, Math.min(4, view.scale * factor));
      reheat(0); // redraw at the new zoom (no physics change).
    }, { passive: false });

    // Slider/mode changes go through load()'s cache: a slider change is a new key
    // (new k/minSim) so it fetches fresh and resets to a fitted layout, while a
    // plain mode switch reuses the built graph — so semantic↔links is instant
    // after each side has been built once. If the overlay was dismissed (Esc),
    // touching a control re-opens it so the change is actually visible.
    if (reload) reload.addEventListener("click", function () {
      if (!visible() && nav) nav.openGraph();
      load(false);
    });

    // Title filter: typing dims everything except nodes whose title matches and
    // their direct neighbours. Purely a client-side highlight (no refetch), so it
    // just recomputes the sets and redraws.
    var filterInput = getEl("g-graph-filter");
    if (filterInput) {
      filterInput.addEventListener("sl-input", function () {
        filterTerm = filterInput.value || "";
        applyFilter();
        reheat(0); // redraw without re-running the layout.
      });
    }
    // The input-bar Graph button opens/focuses the graph tab.
    var openBtn = getEl("g-open-graph");
    if (openBtn) openBtn.addEventListener("click", function () { if (nav) nav.openGraph(); });
    // Re-fit the backing store to the new window size and redraw, even when the
    // layout is parked (maximize/restore while settled would otherwise leave a
    // stale, stretched buffer).
    window.addEventListener("resize", function () {
      if (!visible()) return;
      resize();
      reheat(0);
    });

    // Re-read the theme colours and redraw when the theme changes. The graph
    // caches its palette (refreshColors) and stops drawing once settled, so
    // without this a switch leaves node labels/edges in the old theme's colours.
    document.addEventListener("grimoire:theme", function () {
      refreshColors();
      reheat(0); // force one redraw with the new colours (no physics re-run).
    });

    // The × closes the graph tab (the focused tab when the overlay is up). It's
    // hidden in the Vaults tab's graph view, which has no tab to close — you leave
    // that view by picking another sidebar tab.
    var closeBtn = overlay.querySelector(".g-graph-close");
    if (closeBtn) closeBtn.addEventListener("click", function () { if (nav) nav.closeFocused(); });

    // The first show builds the layout; later shows (re-selecting the tab) resume the
    // cached one with its zoom/pan intact. The reload trigger (a slider change) is the
    // only thing that rebuilds. Visibility is the plain g-graph-open class (setGraph).
    function visible() { return overlay.classList.contains("g-graph-open"); }
    // ready reports the overlay is genuinely on screen AND laid out: shown and with a
    // real box. Gate on the OVERLAY box, not the canvas — a <canvas> keeps a default
    // 300×150 intrinsic size even while #g-graph is collapsed, a false "ready" that
    // kept loading into a zero-size panel.
    function ready() {
      if (!visible()) return false;
      var r = overlay.getBoundingClientRect();
      return r.width >= 1 && r.height >= 1;
    }
    // showGraph is the module hook render() calls after revealing the graph overlay.
    // It waits (per frame, capped) for the overlay to be shown AND sized before it
    // builds or resumes, so the load's spinner and the drawn graph always land in a
    // real, visible box. On a cold process launch the panel is 0×0 until WebView2
    // settles its first layout — this waits that out.
    showGraph = function () {
      var tries = 0;
      (function check() {
        if (!ready()) {
          if (++tries > 180) return; // ~3s cap; give up rather than spin forever.
          requestAnimationFrame(check);
          return;
        }
        if (nodes.length) { resize(); reheat(0); } // resume cached layout, just redraw.
        else load();                                // first load, now there's a real box.
      })();
    };
    // Launch path: when the restored focused tab is the graph, the page renders the
    // overlay already open (g-graph-open seeded server-side), so kick the first build
    // here too in case render() ran before this hook was assigned.
    if (visible()) showGraph();
    // Re-fit a built graph when its box changes (window resize, sidebar collapse) so
    // a parked layout isn't left stretched or clipped.
    var lastW = 0, lastH = 0;
    new ResizeObserver(function () {
      if (!visible() || !nodes.length) return;
      var rect = canvas.getBoundingClientRect();
      var w = Math.floor(rect.width), h = Math.floor(rect.height);
      if (w < 1 || h < 1 || (w === lastW && h === lastH)) return;
      lastW = w; lastH = h;
      fitToContent();
      reheat(0);
    }).observe(canvas);
  }

  // Body editor: a deliberate edit mode (the header pencil — no accidental
  // double-click-to-edit) that swaps the rendered note for a raw-Markdown
  // textarea. Save writes explicitly (button or Ctrl+S), preserving frontmatter;
  // the server re-renders and we drop back to reading. Cancel/toggle-off exits.
  function initEditor() {
    var toggle = getEl("g-edit-toggle");
    var editor = getEl("g-editor");
    var body = getEl("g-preview-body");
    var text = getEl("g-editor-text");
    var dirty = getEl("g-editor-dirty");
    if (!toggle || !editor || !body || !text) return;
    var editing = false, baseline = "";
    var restoring = false; // true while reopening with cached text (don't auto-exit).

    function enter(withText) {
      var raw = getEl("g-raw-body");
      baseline = raw ? raw.value : "";
      text.value = typeof withText === "string" ? withText : baseline;
      editing = true;
      // Editing commits the note's preview tab to a permanent one (IDE-style), so a
      // single-click elsewhere doesn't evict the note you're working in.
      if (nav) nav.pinFocused();
      editor.classList.add("g-editor-open");
      body.style.display = "none";
      toggle.name = "eye"; // now toggles back to reading.
      toggle.title = "Done editing";
      markDirty();
      text.focus();
    }
    function exit() {
      editing = false;
      editor.classList.remove("g-editor-open");
      body.style.display = "";
      toggle.name = "pencil";
      toggle.title = "Edit note";
    }
    function markDirty() {
      if (dirty) dirty.textContent = text.value !== baseline ? "Unsaved" : "";
    }
    function save() {
      setSignal("gNotePath", getSignal("gPreviewPath"));
      setSignal("gBody", text.value);
      baseline = text.value;
      markDirty();
      // Server re-renders #g-preview-body; drop back to reading on the next frame.
      requestAnimationFrame(function () {
        getEl("g-body-save-trigger").click();
        exit();
      });
    }

    toggle.addEventListener("click", function () { editing ? exit() : enter(); });
    getEl("g-editor-save").addEventListener("click", save);
    getEl("g-editor-cancel").addEventListener("click", exit);
    text.addEventListener("input", markDirty);
    text.addEventListener("keydown", function (e) {
      if ((e.ctrlKey || e.metaKey) && (e.key === "s" || e.key === "S")) { e.preventDefault(); save(); }
      if (e.key === "Escape") { e.preventDefault(); exit(); }
    });
    // Opening a different note while editing exits edit mode (the body re-renders).
    // A programmatic restore (re-render then reopen with cached text) sets the
    // `restoring` guard so this observer doesn't immediately exit it.
    new MutationObserver(function () { if (editing && !restoring) exit(); })
      .observe(body, { childList: true });

    // Reopen the editor with cached unsaved text after a note tab is refocused
    // (the body has just re-rendered). Best-effort: skipped if elements are gone.
    editorAPI = {
      restore: function (cachedText) {
        restoring = true;
        enter(cachedText);
        requestAnimationFrame(function () { restoring = false; });
      },
    };
  }

  // Keyboard-shortcuts reference: open from the header button or by pressing "?"
  // anywhere outside a text field. On macOS the Ctrl chips are swapped for ⌘ so
  // they match the real modifier (the handlers already accept metaKey).
  function initShortcuts() {
    var dialog = getEl("g-shortcuts-dialog");
    var btn = getEl("g-shortcuts-btn");
    if (!dialog) return;

    if (navigator.platform && /Mac|iP(hone|ad|od)/.test(navigator.platform)) {
      dialog.querySelectorAll(".g-kbd").forEach(function (k) {
        if (k.textContent === "Ctrl") k.textContent = "⌘";
      });
    }

    function open() { dialog.show(); }
    if (btn) btn.addEventListener("click", open);

    document.addEventListener("keydown", function (e) {
      // Esc closes the dialog (and stops other Esc handlers, so it doesn't also
      // touch the preview's history). Capture phase so it wins the race.
      if (e.key === "Escape" && dialog.open) {
        e.preventDefault();
        e.stopPropagation();
        dialog.hide();
        return;
      }
      if (e.key !== "?" || e.ctrlKey || e.metaKey || e.altKey) return;
      if (typing(e.target)) return;
      e.preventDefault();
      dialog.open ? dialog.hide() : open();
    }, true);
  }

  // Keyboard navigation of the Sessions and Files lists: ↑/↓ move a selection
  // highlight through the active tab's visible rows, Enter opens the selected
  // row (a note/session opens, a folder toggles), and ←/→ collapse/expand a
  // selected folder. The selection is purely visual (.g-kbd-sel) and drives the
  // same actions a click does, so it routes through the existing navigator.
  // Arrows work from the filter inputs too, so you can type to filter then arrow
  // into the results without leaving the field.
  function initKeyboardNav() {
    var sessions = getEl("g-sessions");
    var files = getEl("g-files");
    // #g-files holds either the vault tree or the trash, swapped in place by the
    // toolbar Trash toggle (a server re-render) — trash is "the same file view
    // opened in a special folder". So all the selection/nav/preview machinery
    // here targets #g-files unchanged; only the batch actions read the mode.

    // Multi-selection state, per list. We store stable KEYS (a session's id, a
    // note's path) rather than row elements, so the selection survives the server
    // re-rendering the list (rows are recreated). anchor is the key a Shift-range
    // extends from. Folders aren't multi-selectable — only notes and sessions.
    var multi = { sessions: new Set(), files: new Set() };
    var anchor = { sessions: null, files: null };
    function bag(list) { return list === sessions ? multi.sessions : multi.files; }
    function setAnchor(list, key) { if (list === sessions) anchor.sessions = key; else anchor.files = key; }
    function getAnchor(list) { return list === sessions ? anchor.sessions : anchor.files; }

    // rowKey is a row's stable identity; selectableRow is a row eligible for
    // multi-select (sessions; notes — not folders).
    function rowKey(row) { return row.getAttribute(row.classList.contains("g-session") ? "data-id" : "data-note"); }
    function selectableRow(list, row) {
      return row && (list === sessions ? row.classList.contains("g-session") : row.classList.contains("g-tree-note"));
    }
    function rowByKey(list, key) {
      var sel = list === sessions ? '.g-session[data-id="' : '.g-tree-note[data-note="';
      return list.querySelector(sel + cssEscape(key) + '"]');
    }

    // shown reports whether el is laid out (visible) — positioning-independent,
    // unlike offsetParent which is null for position:fixed/sticky elements.
    function shown(el) {
      return !!(el && (el.offsetWidth || el.offsetHeight || el.getClientRects().length));
    }

    // activeList returns the list the user is currently looking at (the visible
    // tab's container), or null if neither is shown.
    function activeList() {
      if (shown(sessions)) return sessions;
      if (shown(files)) return files;
      return null;
    }

    // visibleRow reports whether a row is currently navigable: not mid-rename, not
    // hidden by the filter (display:none), and — for the tree — not tucked inside a
    // collapsed folder. The ancestor-<details> check is deterministic, unlike a
    // layout measurement that can read stale right after a render.
    function visibleRow(el) {
      if (el.querySelector(".g-session-edit,.g-tree-edit")) return false;
      if (el.style.display === "none") return false;
      var d = el.closest(".g-tree-children");
      while (d) {
        var details = d.parentElement; // the enclosing <details>.
        if (details && details.tagName === "DETAILS" && !details.open) return false;
        d = details ? details.parentElement.closest(".g-tree-children") : null;
      }
      return true;
    }

    // rowsOf returns the openable rows of a list in visual (document) order,
    // skipping any hidden by a filter or a collapsed folder. For sessions that's
    // the session rows; for files it's folder rows and note rows (not greyed ones).
    function rowsOf(list) {
      var sel = list === sessions
        ? ".g-session"
        : ".g-tree-folder-row,.g-tree-note";
      return Array.prototype.filter.call(list.querySelectorAll(sel), visibleRow);
    }

    function selected(list) { return list.querySelector(".g-kbd-sel"); }

    // select marks a row as the keyboard selection and moves real DOM focus to it,
    // so the last thing acted on (Tab control vs. arrowed row) is the focused
    // element — which is what Delete and the rest key off. Rows are tabindex="-1",
    // focusable programmatically but skipped by Tab.
    function select(list, row) {
      var prev = selected(list);
      if (prev) prev.classList.remove("g-kbd-sel");
      if (row) {
        row.classList.add("g-kbd-sel");
        row.scrollIntoView({ block: "nearest" });
        row.focus({ preventScroll: true });
      }
    }

    // ── multi-selection ──
    // applyMulti paints .g-multi-sel from the key set (after a click or a list
    // re-render) and refreshes the action bar.
    function applyMulti(list) {
      var keys = bag(list);
      list.querySelectorAll(".g-multi-sel,.g-multi-top,.g-multi-bot").forEach(function (r) {
        r.classList.remove("g-multi-sel", "g-multi-top", "g-multi-bot");
      });
      keys.forEach(function (key) {
        var row = rowByKey(list, key);
        if (row) row.classList.add("g-multi-sel");
      });
      // Round only the outer corners of each contiguous run: walk the visible rows
      // in order and tag the first/last selected row of each unbroken stretch.
      var rows = rowsOf(list), inRun = false;
      for (var i = 0; i < rows.length; i++) {
        var on = rows[i].classList.contains("g-multi-sel");
        if (on && !inRun) rows[i].classList.add("g-multi-top");
        if (on && (i === rows.length - 1 || !rows[i + 1].classList.contains("g-multi-sel"))) rows[i].classList.add("g-multi-bot");
        inRun = on;
      }
      updateBar(list);
    }
    function clearMulti(list) {
      if (bag(list).size === 0) return false;
      bag(list).clear();
      setAnchor(list, null);
      applyMulti(list);
      return true;
    }
    function toggleKey(list, key) {
      var keys = bag(list);
      if (keys.has(key)) keys.delete(key); else keys.add(key);
    }
    // selectRange replaces the selection with every selectable row between the
    // anchor and the target (inclusive), in visual order.
    function selectRange(list, targetRow) {
      var rows = rowsOf(list).filter(function (r) { return selectableRow(list, r); });
      var anchorRow = getAnchor(list) ? rowByKey(list, getAnchor(list)) : null;
      var a = anchorRow ? rows.indexOf(anchorRow) : -1;
      var b = rows.indexOf(targetRow);
      if (b === -1) return;
      if (a === -1) a = b;
      var lo = Math.min(a, b), hi = Math.max(a, b);
      var keys = bag(list);
      keys.clear();
      for (var i = lo; i <= hi; i++) keys.add(rowKey(rows[i]));
      applyMulti(list);
    }
    // Action bar: count + Delete/Restore/Clear, shown only when something is
    // selected. Files and trash share #g-files-actions — in trash mode the bar's
    // CSS (under g-files-trashing) swaps Delete's meaning and reveals Restore.
    function updateBar(list) {
      var barID = list === sessions ? "g-sessions-actions" : "g-files-actions";
      var bar = getEl(barID);
      if (!bar) return;
      var n = bag(list).size;
      bar.classList.toggle("g-actions-open", n > 0);
      var count = bar.querySelector(".g-actions-count");
      if (count) count.textContent = n + (list === sessions ? (n === 1 ? " session" : " sessions") : (n === 1 ? " note" : " notes")) + " selected";
    }
    // selectedKeys returns the current multi-selection as an array (batch payload).
    // bag(list) is a Set — use Array.from (slice.call on a Set yields []).
    function selectedKeys(list) { return Array.from(bag(list)); }

    // move steps the selection by delta through the visible rows. With nothing
    // selected it seeds from the active session (sessions) or the first row, and
    // moving past either end stays clamped on the last/first row (never clears).
    function move(list, delta) {
      var rows = rowsOf(list);
      if (!rows.length) return;
      var cur = selected(list);
      var i = cur ? rows.indexOf(cur) : -1;
      if (i === -1) {
        // No selection (or it scrolled out of the visible set): seed at an end so
        // the first arrow lands on a real row instead of clearing the selection.
        var active = list.querySelector(".g-session-active");
        i = active ? rows.indexOf(active) : (delta > 0 ? -1 : rows.length);
      }
      i = Math.max(0, Math.min(rows.length - 1, i + delta));
      select(list, rows[i]);
    }

    // Enter opens the keyboard-selected row as a permanent (pinned) tab — a
    // deliberate open, unlike a single mouse click which previews.
    function openRow(row) {
      if (row.classList.contains("g-session")) {
        if (nav) nav.openSessionPinned(row.getAttribute("data-id"), row.getAttribute("data-open-url"), row.getAttribute("data-title"));
      } else if (row.classList.contains("g-tree-note")) {
        if (nav) nav.openNotePinned(row.getAttribute("data-note"), "");
      } else if (row.classList.contains("g-tree-folder-row")) {
        var details = row.closest(".g-tree-folder");
        if (details) details.open = !details.open;
      }
    }

    // setFolder opens or closes the folder of a selected folder row.
    function setFolder(row, open) {
      var details = row.closest(".g-tree-folder");
      if (details) details.open = open;
    }

    // Ctrl/Cmd+A selects every selectable row in the active list (the visible
    // notes, or the sessions). Skipped while typing so it doesn't hijack a text
    // field's own select-all — unless focus is in the list filter.
    document.addEventListener("keydown", function (e) {
      if (!(e.ctrlKey || e.metaKey) || e.key !== "a" && e.key !== "A") return;
      if (typing(e.target) && !inListFilter(e.target)) return;
      var list = activeList();
      if (!list) return;
      var rows = rowsOf(list).filter(function (r) { return selectableRow(list, r); });
      if (!rows.length) return;
      e.preventDefault();
      var keys = bag(list);
      keys.clear();
      rows.forEach(function (r) { keys.add(rowKey(r)); });
      setAnchor(list, rowKey(rows[0]));
      applyMulti(list);
    });

    document.addEventListener("keydown", function (e) {
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      var keys = ["ArrowDown", "ArrowUp", "ArrowLeft", "ArrowRight", "Enter"];
      if (keys.indexOf(e.key) === -1) return;

      // Allow driving the list from its filter input; otherwise bail when typing.
      var inFilter = inListFilter(e.target);
      if (typing(e.target) && !inFilter) return;
      // From a filter, only the vertical moves and Enter act; ←/→ stay text edits.
      if (inFilter && (e.key === "ArrowLeft" || e.key === "ArrowRight")) return;

      var list = activeList();
      if (!list) return;

      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        // Anchor before the move so Shift+arrow extends from where the cursor was.
        var from = selected(list);
        if (e.shiftKey && !getAnchor(list) && from && selectableRow(list, from)) setAnchor(list, rowKey(from));
        move(list, e.key === "ArrowDown" ? 1 : -1);
        var cur = selected(list);
        if (e.shiftKey && cur && selectableRow(list, cur)) {
          selectRange(list, cur); // extend the range to the new cursor.
        } else {
          // Plain move: clear any multi-selection and re-anchor at the cursor.
          if (bag(list).size) clearMulti(list);
          if (cur && selectableRow(list, cur)) setAnchor(list, rowKey(cur));
        }
        return;
      }

      var row = selected(list);
      if (!row) return;
      if (e.key === "Enter") { e.preventDefault(); openRow(row); return; }
      if (list === files && row.classList.contains("g-tree-folder-row")) {
        if (e.key === "ArrowRight") { e.preventDefault(); setFolder(row, true); }
        else if (e.key === "ArrowLeft") { e.preventDefault(); setFolder(row, false); }
      }
    });

    // Esc works in layers: first drop a multi-selection, then the keyboard cursor,
    // and finally — if we're in the trash view — exit it back to the tree. It only
    // consumes the key when it actually did one of these; otherwise Esc falls
    // through to whatever other overlay (find bar, dialog) wants it.
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape" || typing(e.target)) return;
      var cleared = false;
      [sessions, files].forEach(function (list) {
        if (!list) return;
        if (clearMulti(list)) cleared = true;        // drop a multi-selection first,
        if (selected(list)) { select(list, null); cleared = true; } // then the cursor.
      });
      // Last layer: with nothing selected, Esc leaves the trash view.
      if (!cleared && exitTrashMode && exitTrashMode()) cleared = true;
      if (cleared) { e.preventDefault(); e.stopPropagation(); }
    }, true);

    // Clicking a row makes it the keyboard cursor (so arrows continue from there).
    // Ctrl/Cmd+click toggles it in the multi-selection; Shift+click selects the
    // range from the anchor; a plain click clears the multi-selection.
    [sessions, files].forEach(function (list) {
      if (!list) return;
      list.addEventListener("click", function (e) {
        // Ignore clicks on a row's inline controls (delete ×, rename input, the
        // trash restore/delete icons, etc.).
        if (e.target.closest(".g-session-del,.g-tree-del,.g-tree-del-folder,.g-tree-add,.g-session-edit,.g-tree-edit,.g-trash-restore")) return;
        var row = e.target.closest(list === sessions ? ".g-session" : ".g-tree-folder-row,.g-tree-note");
        if (!row || !list.contains(row)) return;
        if ((e.ctrlKey || e.metaKey) && selectableRow(list, row)) {
          toggleKey(list, rowKey(row));
          setAnchor(list, rowKey(row));
          applyMulti(list);
          select(list, row);
        } else if (e.shiftKey && selectableRow(list, row)) {
          if (!getAnchor(list)) setAnchor(list, rowKey(row));
          selectRange(list, row);
          select(list, row);
        } else {
          clearMulti(list);
          // A plain click anchors the range here, so a following Shift+click
          // selects from this row in either direction.
          if (selectableRow(list, row)) setAnchor(list, rowKey(row));
          select(list, row);
        }
      });
      // When focus leaves the list (Tab to a control, click elsewhere), drop the
      // cursor so only the genuinely focused element is highlighted; the multi-
      // selection persists (it's an explicit set, not focus-driven).
      list.addEventListener("focusout", function (e) {
        if (!e.relatedTarget || !list.contains(e.relatedTarget)) select(list, null);
      });
      // The list re-renders on every server mutation; re-paint the multi-selection
      // (and refresh the bar) so it survives — keys that no longer exist drop off.
      new MutationObserver(function () {
        var keys = bag(list);
        keys.forEach(function (key) { if (!rowByKey(list, key)) keys.delete(key); });
        applyMulti(list);
      }).observe(list, { childList: true });
    });

    // Clicking anywhere outside a list (and its action bar) drops that list's
    // multi-selection. The list's own click handler manages in-list clicks; the
    // action bar is excluded so its Delete/Clear buttons can act on the selection.
    document.addEventListener("click", function (e) {
      [["g-sessions", "g-sessions-actions"], ["g-files", "g-files-actions"]].forEach(function (pair) {
        var list = getEl(pair[0]);
        if (!list || !bag(list).size) return;
        if (e.target.closest("#" + pair[0] + ",#" + pair[1])) return; // inside the list/bar.
        clearMulti(list);
      });
    });

    // Exposed so the trash mode toggle can drop the files selection when switching
    // between the tree and the trash (a selection shouldn't carry across the mode
    // change — the rows are different).
    clearFilesSelection = function () { if (files) clearMulti(files); };

    initBatchActions(sessions, files, {
      bag: bag, selectedKeys: selectedKeys, clearMulti: clearMulti,
    });
  }

  // batchDeleteSelection is set by initBatchActions and called by the per-list
  // Delete-key handlers when that list has a multi-selection — so the keyboard
  // Delete and the action-bar Delete button share one code path.
  var batchDeleteSelection = null;
  // Set by initKeyboardNav; drops the files multi-selection. Called when toggling
  // trash mode so a selection doesn't bleed across the mode change.
  var clearFilesSelection = null;
  // Set by initTrash; leaves the trash view (the last Esc layer). Returns true if
  // it was in trash mode and exited.
  var exitTrashMode = null;

  // Batch actions on a multi-selection: the action bars' Delete/Restore/Clear
  // buttons, plus the batch-delete used by the keyboard Delete key. sel exposes the
  // selection state owned by initKeyboardNav. The files bar serves both the tree
  // and the trash (they share #g-files): in trash mode Delete means permanent
  // removal and Restore is available, both keyed by the rows' data-trash-id.
  function initBatchActions(sessions, files, sel) {
    function inTrashMode() {
      var section = getEl("g-files-section");
      return !!(section && section.classList.contains("g-files-trashing"));
    }
    // selectedTrashIDs maps the files selection (keyed by each row's data-note
    // trash path) to the rows' data-trash-id — what the bulk trash endpoints want.
    function selectedTrashIDs() {
      return sel.selectedKeys(files).map(function (path) {
        var row = files.querySelector('.g-tree-note[data-note="' + cssEscape(path) + '"]');
        return row ? row.getAttribute("data-trash-id") : "";
      }).filter(Boolean);
    }

    // batchDelete posts the selected keys as a JSON array to the batch endpoints,
    // after one confirmation that names the count. In the trash, "delete" means
    // permanent removal (by trash id).
    function batchDelete(list) {
      if (list === files && inTrashMode()) {
        var ids = selectedTrashIDs();
        if (!ids.length) return;
        confirmDelete.ask("Delete " + ids.length + (ids.length === 1 ? " note" : " notes"),
          "Permanently delete " + ids.length + (ids.length === 1 ? " note" : " notes") + " from the trash? This can't be undone.", function () {
            fireWithSignal("gTrashIDs", JSON.stringify(ids), "g-trash-delete-many-trigger");
            sel.clearMulti(files);
          });
        return;
      }
      var keys = sel.selectedKeys(list);
      if (!keys.length) return;
      var isSessions = list === sessions;
      var noun = (isSessions ? "session" : "note") + (keys.length === 1 ? "" : "s");
      confirmDelete.ask("Delete " + keys.length + " " + noun,
        "Delete " + keys.length + " " + noun + "? This can't be undone.", function () {
          if (isSessions) {
            fireWithSignal("gBatchIDs", JSON.stringify(keys), "g-sessions-delete-many-trigger");
            if (nav) keys.forEach(function (id) { nav.closeSession(id); });
          } else {
            fireWithSignal("gBatchPaths", JSON.stringify(keys), "g-note-delete-many-trigger");
            if (nav) keys.forEach(function (p) { nav.closeNote(p); });
          }
          sel.clearMulti(list);
        });
    }
    // batchRestore returns the selected trashed notes to the tree (no confirm —
    // restoring is non-destructive).
    function batchRestore() {
      var ids = selectedTrashIDs();
      if (!ids.length) return;
      fireWithSignal("gTrashIDs", JSON.stringify(ids), "g-trash-restore-many-trigger");
      sel.clearMulti(files);
    }
    // Expose it for the keyboard Delete handlers (which live in initSessions /
    // initFileActions and reliably fire). Returns true if it handled a selection.
    batchDeleteSelection = function (list) {
      if (!sel.bag(list).size) return false;
      batchDelete(list);
      return true;
    };

    // Wire one action bar's Delete and Clear buttons.
    function wireBar(list, barId) {
      var bar = getEl(barId);
      if (!bar) return;
      var del = bar.querySelector(".g-actions-del");
      if (del) del.addEventListener("click", function () { batchDelete(list); });
      var clear = bar.querySelector(".g-actions-clear");
      if (clear) clear.addEventListener("click", function () { sel.clearMulti(list); });
    }
    wireBar(sessions, "g-sessions-actions");
    wireBar(files, "g-files-actions");

    // The files bar's Restore button (shown only in trash mode) restores the
    // selection.
    var filesBar = getEl("g-files-actions");
    if (filesBar) {
      var restore = filesBar.querySelector(".g-actions-restore");
      if (restore) restore.addEventListener("click", batchRestore);
    }
  }

  // typing reports whether the event target is a field that should keep its own
  // keystrokes (so global shortcuts don't steal them).
  function typing(el) {
    if (!el) return false;
    var tag = el.tagName;
    return el.isContentEditable || tag === "INPUT" || tag === "TEXTAREA" ||
      tag === "SL-INPUT" || tag === "SL-TEXTAREA";
  }

  // inListFilter reports whether the event target is one of the list filter inputs.
  // Arrow nav and Delete are allowed to act on the selected row from there, so you
  // can Tab to a filter, arrow into the list, and delete without leaving the field.
  function inListFilter(el) {
    return !!(el && el.closest && el.closest("#g-session-filter,#g-files-filter"));
  }

  // Tab persistence: remember the active sidebar tab across a reload (F5) so the
  // page comes back where it was instead of the default (Vaults). Files and
  // Sessions are pure navigators — switching between them never touches the main
  // panel; they just open things into the workspace tabs. Vaults is the exception:
  // it takes the panel over with the vault's similarity graph (nav.syncVaultGraph).
  var TAB_KEY = "grimoire.tab";
  function saveActiveTab(name) {
    try { sessionStorage.setItem(TAB_KEY, name); } catch (e) { /* best-effort. */ }
  }
  function initSidebarTabs() {
    var group = getEl("g-tabs");
    var newBtn = getEl("g-tab-new");
    // The strip "+" tooltip reflects what it'll do for the active sidebar tab
    // (new note on Files, add vault on Vaults, new session otherwise; the click
    // handler is in initPreview).
    var NEW_TITLES = { files: "New note", vaults: "Add vault" };
    function syncNewTitle(name) {
      if (newBtn) newBtn.title = NEW_TITLES[name] || "New session";
    }
    if (group) group.addEventListener("sl-tab-show", function (e) {
      saveActiveTab(e.detail.name);
      syncNewTitle(e.detail.name);
      // Vaults shows that vault's similarity graph in place of the workspace;
      // leaving it puts the workspace back untouched.
      if (nav) nav.syncVaultGraph();
    });
    syncNewTitle(group && group.activeTab ? group.activeTab.panel : "sessions");
  }
  // restoreActiveTab switches to the saved tab and returns a promise that resolves
  // once the switch has actually rendered, so the caller can hold the page hidden
  // until then (no Sessions flash). show() runs the panel sync; call it once the
  // group has rendered. Don't pre-set the tab's active attribute — that makes the
  // group think the tab is already active and skip the panel sync.
  var SIDEBAR_PANELS = { vaults: true, sessions: true, files: true };
  function restoreActiveTab() {
    var group = getEl("g-tabs");
    if (!group) return Promise.resolve();
    // Always wait out the group's first render, even when there is nothing to
    // show(): the caller reads group.activeTab next (the Vaults-graph rule), and
    // on a fast reload that property is unset until this promise settles.
    var ready = group.updateComplete && group.updateComplete.then
      ? group.updateComplete : Promise.resolve();
    var name;
    try { name = sessionStorage.getItem(TAB_KEY); } catch (e) { return ready; }
    // Anything that isn't a sidebar panel is a stale value ("graph" moved to the
    // main panel); leave the group on its default rather than showing nothing.
    if (!SIDEBAR_PANELS[name]) return ready;
    if (typeof group.show !== "function") return ready;
    return ready.then(function () {
      group.show(name);
      return group.updateComplete || Promise.resolve(); // wait for the switch to render.
    });
  }

  function init() {
    confirmDelete.init();
    initVault();
    vaults.init();
    themePicker.init();
    extensions.init();
    initTrashSwitch();
    initSearch();
    initSearchParams();
    initSidebarCollapse();
    initSidebarTabs();
    calmHoverWhileScrolling("g-sessions");
    calmHoverWhileScrolling("g-files");
    calmHoverWhileScrolling("g-vaults");
    initResize();
    initAutoScroll();
    initPreview();
    initSessions();
    initTurnMenu();
    initFind();
    initFiles();
    initTrash();
    initFileActions();
    initActiveNote();
    initPreviewUntabbable();
    initDragMove();
    initProps();
    initEditor();
    initShortcuts();
    initCopy();
    initRun();
    initKeyboardNav();
    initGraph();
    // Restore the view open before a reload (F5), now that every init*() has
    // registered its listeners. The active tab was already restored in boot()
    // (earlier, to avoid a flash), but re-assert it in case init's openGraph etc.
    // shifted it.
    // Restore the saved tab + view, then reveal the app — held hidden pre-paint so
    // neither the default Sessions tab nor the empty home flashes first.
    restoreActiveTab().then(function () {
      // Apply the Vaults-tab graph rule for the restored sidebar tab. Shoelace
      // emits sl-tab-show only on a change, so a tab that was already active
      // (the default, or a show() that was a no-op) needs this one call. Running
      // it before the workspace restore also means the graph view never paints a
      // focused tab first.
      if (nav) nav.syncVaultGraph();
      // navRestore (restoreTabs) fetches the persisted tabs server-side; reveal
      // only once it resolves so the restored view doesn't flash in after paint.
      var done = navRestore ? navRestore() : null;
      if (done && typeof done.then === "function") done.then(finishRestore, finishRestore);
      else finishRestore();
    });
  }

  function finishRestore() {
    revealMain();
    openPendingNote();
  }

  // ?note= is how a cross-vault search result opens: clicking a hit from another
  // vault navigates here with the note named, since the page's panels all speak
  // to one vault. Open it on top of the restored tabs, then drop it from the URL
  // so a later refresh restores the workspace instead of reopening the note.
  function openPendingNote() {
    var match = /[?&]note=([^&]*)/.exec(location.search);
    if (!match) return;
    if (nav) nav.openNote(decodeURIComponent(match[1].replace(/\+/g, "%20")), "");
    if (window.history && history.replaceState) {
      var search = location.search.replace(/([?&])note=[^&]*&?/, "$1").replace(/[?&]$/, "");
      history.replaceState(null, "", location.pathname + search + location.hash);
    }
  }

  // Boot gates on the SDK layout's massDatastarReady, not on a delay after
  // DOMContentLoaded: Datastar loads via an async head module (it must wait for
  // Shoelace upgrades), and WebKit fires DOMContentLoaded without waiting for
  // that module's awaits. Booting earlier made init's workspace restore fire
  // the preview signal + trigger before data-bind/data-on listeners existed,
  // so a restored note tab came back focused but empty on every app restart
  // (an F5 masked it: restoreActiveTab's sl-tab-group wait bought enough time).
  // The race caps the wait so a failed Datastar import can't leave a dead app;
  // readiness also implies Shoelace is upgraded (the module awaits it first).
  function boot() {
    var ready = window.massDatastarReady || Promise.resolve();
    Promise.race([ready, new Promise(function (r) { setTimeout(r, 4000); })]).then(init);
    setTimeout(revealMain, 5000); // safety: never leave the app hidden if init stalls.
  }

  // Reload keys. The window is a webview with no browser chrome, so F5 and
  // Ctrl/Cmd+R (Shift variants included — the browser convention for a hard
  // reload) have to be wired by hand; WebKit's own handling of them is partial.
  // A plain reload is the right "refresh the view" semantics here: the page
  // restores the active sidebar tab (restoreActiveTab, sessionStorage) and the
  // workspace tabs (navRestore, server-side uistate) on boot. Registered at
  // script load rather than in init(), so the keys still work if boot stalls —
  // which is exactly when a refresh is wanted.
  function initReload() {
    document.addEventListener("keydown", function (e) {
      if (e.altKey) return;
      if (e.key !== "F5" && !((e.ctrlKey || e.metaKey) && (e.key === "r" || e.key === "R"))) return;
      e.preventDefault();
      location.reload();
    });
  }

  // Pre-paint: hide the whole app until the persisted workspace is restored, so
  // neither the default Sessions tab nor the empty home flashes before the saved
  // tabs/view swap in. Tab state now lives server-side (per-vault SQLite), so it
  // can't be probed synchronously; we always hold and reveal once restore resolves
  // (a local DB read is near-instant, and a 1500ms safety timeout reveals anyway).
  // Stacks on top of the SDK's body-until-Shoelace-ready gate. Runs at script load
  // (end-of-body, before dynamic content paints).
  function hideMainUntilRestore() {
    var app = getEl("app-grimoire");
    if (app) app.classList.add("g-prepaint-hide");
  }
  function revealMain() {
    var app = getEl("app-grimoire");
    if (app) app.classList.remove("g-prepaint-hide");
  }
  hideMainUntilRestore();
  initReload();

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();
