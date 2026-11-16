(function () {
  var NAV = [
    { title: "Get started", pages: ["introduction", "installation", "quickstart", "concepts"] },
    { title: "Build guides", pages: ["tools", "sessions", "context", "approvals", "structured-output", "streaming"] },
    { title: "Run guides", pages: ["security", "authorization", "sandboxes", "sealed", "observability"] },
    { title: "Components", pages: ["providers", "mcp", "a2a", "multiagent", "evals", "triggers", "memory-skills"] },
    { title: "Examples", pages: ["gbot"] },
    { title: "Reference", pages: ["reference-api", "status"] }
  ];
  var ICON = {
    copy: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>',
    check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true"><path d="m5 12 5 5 9-10"/></svg>',
    chev: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="m9 6 6 6-6 6"/></svg>',
    file: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/></svg>',
    term: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="m5 8 4 4-4 4M12 17h7"/></svg>'
  };

  var pages = Object.create(null), order = [], groupOf = Object.create(null);
  document.querySelectorAll("section.page").forEach(function (s) { pages[s.id] = s; });
  NAV.forEach(function (g) {
    g.pages.forEach(function (id) {
      if (pages[id]) { order.push(id); groupOf[id] = g.title || ""; }
    });
  });
  function title(id) {
    var p = pages[id];
    if (!p) return id;
    if (p.dataset.nav) return p.dataset.nav;
    var h = p.querySelector("h1");
    return h ? h.textContent.trim() : id;
  }

  // ---- sidebar ----
  var sidenav = document.getElementById("sidenav");
  var openGroups = {};
  try { openGroups = JSON.parse(localStorage.getItem("kritrim-nav-open") || "{}") || {}; } catch (e) {}
  function saveGroups() { try { localStorage.setItem("kritrim-nav-open", JSON.stringify(openGroups)); } catch (e) {} }
  if (sidenav) {
    NAV.forEach(function (g) {
      if (!g.title) {
        g.pages.forEach(function (id) {
          if (pages[id]) sidenav.insertAdjacentHTML("beforeend", '<a class="item top-item" href="#' + id + '">' + title(id) + "</a>");
        });
        return;
      }
      var open = openGroups[g.title] !== undefined ? !!openGroups[g.title] : true;
      var html = '<div class="group" data-open="' + open + '"><button type="button" aria-expanded="' + open + '">' + g.title + ICON.chev + '</button><div class="items">';
      g.pages.forEach(function (id) {
        if (pages[id]) html += '<a class="item" href="#' + id + '">' + title(id) + "</a>";
      });
      sidenav.insertAdjacentHTML("beforeend", html + "</div></div>");
    });
    sidenav.querySelectorAll(".group > button").forEach(function (b) {
      b.addEventListener("click", function () {
        var g = b.parentNode, open = g.dataset.open !== "true";
        g.dataset.open = String(open);
        b.setAttribute("aria-expanded", String(open));
        openGroups[b.textContent.trim()] = open;
        saveGroups();
      });
    });
  }

  // ---- page chrome ----
  order.forEach(function (id) {
    var h1 = pages[id].querySelector("h1");
    if (!h1 || id === "introduction") return;
    var row = document.createElement("div");
    row.className = "titlerow";
    h1.parentNode.insertBefore(row, h1);
    row.appendChild(h1);
    var b = document.createElement("button");
    b.type = "button";
    b.className = "copypage";
    b.innerHTML = ICON.copy + "<span>Copy page</span>";
    b.addEventListener("click", function () {
      var clone = pages[id].cloneNode(true);
      clone.querySelectorAll(".anchor,.copypage,.toc-mobile,.pager,.site-footer,.copy,.hero-actions,.hero-art").forEach(function (x) { x.remove(); });
      copyText(clone.innerText.replace(/\n{3,}/g, "\n\n"), b, "<span>Copied</span>", "<span>Copy page</span>");
    });
    row.appendChild(b);
  });

  // ---- code blocks ----
  document.querySelectorAll(".code").forEach(function (box) {
    var pre = box.querySelector("pre"), code = box.querySelector("code");
    if (!pre || !code) return;
    var label = box.dataset.title || "";
    var isTerm = label === "terminal";
    if (isTerm) box.classList.add("plain");
    try { if (window.hljs && code.className.indexOf("language-") === 0) hljs.highlightElement(code); } catch (e) {}
    var lines = code.textContent.replace(/\n$/, "").split("\n").length;
    var bar = document.createElement("div");
    bar.className = "bar";
    bar.innerHTML = '<span class="file">' + (isTerm ? ICON.term : ICON.file) + "<span></span></span>";
    bar.querySelector(".file span").textContent = label;
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "copy";
    btn.setAttribute("aria-label", "Copy code");
    btn.innerHTML = ICON.copy;
    btn.addEventListener("click", function () { copyText(code.innerText, btn, ICON.check, ICON.copy); });
    bar.appendChild(btn);
    var body = document.createElement("div");
    body.className = "body";
    var nums = document.createElement("div");
    nums.className = "nums";
    nums.setAttribute("aria-hidden", "true");
    var n = [];
    for (var i = 1; i <= lines; i++) n.push(i);
    nums.textContent = n.join("\n");
    box.insertBefore(bar, pre);
    box.insertBefore(body, pre);
    body.appendChild(nums);
    body.appendChild(pre);
  });

  function copyText(text, btn, done, idle) {
    var ok = function () {
      if (typeof idle === "string") btn.innerHTML = btn.innerHTML.replace(idle, done);
      else btn.innerHTML = done;
      setTimeout(function () {
        if (typeof idle === "string") btn.innerHTML = btn.innerHTML.replace(done, idle);
        else btn.innerHTML = idle;
      }, 1400);
    };
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(ok, function () {});
        return;
      }
    } catch (e) {}
    try {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
      ok();
    } catch (e) {}
  }

  // ---- routing ----
  var crumbs = document.getElementById("crumbs"), pager = document.getElementById("pager"), toc = document.getElementById("toc");
  var current = null;
  function pageFor(hash) {
    if (pages[hash]) return { page: hash };
    var el = hash && document.getElementById(hash);
    if (el) { var s = el.closest("section.page"); if (s) return { page: s.id, anchor: el }; }
    return { page: "introduction" };
  }
  function show(hash) {
    var t = pageFor(hash), id = t.page;
    if (current !== id) {
      document.querySelectorAll("section.page").forEach(function (p) { p.hidden = p.id !== id; });
      current = id;
      document.querySelectorAll(".side a.item").forEach(function (a) {
        var on = a.getAttribute("href") === "#" + id;
        if (on) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
        if (on) { var g = a.closest(".group"); if (g) { g.dataset.open = "true"; g.querySelector("button").setAttribute("aria-expanded", "true"); } }
      });
      var grp = groupOf[id];
      if (crumbs) crumbs.innerHTML = grp ? "<span>" + grp + "</span>" + ICON.chev + "<span>" + title(id) + "</span>" : "<span>Docs</span>";
      var i = order.indexOf(id), prev = order[i - 1], next = order[i + 1];
      if (pager) {
        pager.innerHTML = (prev ? '<a href="#' + prev + '"><small>Previous</small><b>' + title(prev) + "</b></a>" : "<span></span>") +
          (next ? '<a class="next" href="#' + next + '"><small>Next</small><b>' + title(next) + "</b></a>" : "");
      }
      var hs = id === "introduction" ? pages[id].querySelectorAll(".section-head h2") : pages[id].querySelectorAll("h2, h3");
      if (toc) {
        toc.innerHTML = "";
        hs.forEach(function (h, k) {
          if (!h.id) h.id = id + "-" + (k + 1);
          var target = h.id;
          var sec = h.closest(".section");
          if (sec && sec.id) target = sec.id;
          var label = h.cloneNode(true);
          label.querySelectorAll(".anchor").forEach(function (x) { x.remove(); });
          var a = document.createElement("a");
          a.href = "#" + target;
          a.textContent = h.getAttribute("data-toc") || label.innerText.trim();
          toc.appendChild(a);
        });
        if (!hs.length) toc.innerHTML = '<a href="#' + id + '" class="on">' + title(id) + "</a>";
      }
      var top = id === "introduction" ? "" : id === "reference-api" || id === "status" ? "api" : "docs";
      document.querySelectorAll(".toplinks a").forEach(function (a) {
        if (a.dataset.top === top) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
      });
      document.title = title(id) + " · kritrim docs";
      document.body.classList.remove("nav-open");
      var mb = document.getElementById("menubtn");
      if (mb) mb.setAttribute("aria-expanded", "false");
      document.body.setAttribute("data-page", id);
      var mainEl = document.getElementById("main");
      if (mainEl && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
        mainEl.classList.remove("page-in");
        void mainEl.offsetWidth;
        mainEl.classList.add("page-in");
      }
    }
    if (t.anchor) {
      var el = t.anchor; // pageFor already found the element
      if (el) setTimeout(function () { el.scrollIntoView({ block: "start" }); }, 0);
    } else {
      window.scrollTo(0, 0);
    }
  }
  function route() { show(location.hash ? location.hash.slice(1) : "introduction"); }
  window.addEventListener("hashchange", route);
  route();

  // ---- theme ----
  var root = document.documentElement;
  try { var saved = localStorage.getItem("kritrim-theme"); if (saved) root.dataset.theme = saved; } catch (e) {}
  var themeBtn = document.getElementById("themebtn");
  var sunIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>';
  var moonIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>';
  function themeIsDark() { return root.dataset.theme !== "light"; }
  function syncTheme() {
    if (!themeBtn) return;
    var dark = themeIsDark();
    themeBtn.innerHTML = dark ? sunIcon : moonIcon;
    themeBtn.setAttribute("aria-pressed", String(dark));
    themeBtn.setAttribute("aria-label", dark ? "Switch to light mode" : "Switch to dark mode");
    document.querySelectorAll('meta[name="theme-color"]').forEach(function (m) { m.setAttribute("content", dark ? "#000000" : "#ffffff"); });
  }
  syncTheme();
  if (themeBtn) themeBtn.addEventListener("click", function () {
    root.dataset.theme = themeIsDark() ? "light" : "dark";
    try { localStorage.setItem("kritrim-theme", root.dataset.theme); } catch (e) {}
    syncTheme();
  });

  // ---- search ----
  var pal = document.getElementById("palette"), pq = document.getElementById("pq"), plist = document.getElementById("plist"), sel = 0, results = [];
  var index = [];
  order.forEach(function (id) {
    index.push({ href: id, label: title(id), where: groupOf[id] || "Docs", text: pages[id].textContent.toLowerCase() });
    pages[id].querySelectorAll("h2, h3").forEach(function (h, k) {
      if (!h.id) h.id = id + "-" + (k + 1);
      var label = h.getAttribute("data-toc") || h.innerText.replace(/\s+/g, " ").trim();
      index.push({ href: h.id, label: label, where: title(id), text: h.textContent.toLowerCase() });
    });
  });
  function render() {
    var q = pq.value.trim().toLowerCase();
    results = index.filter(function (r) { return !q || r.label.toLowerCase().indexOf(q) >= 0 || r.text.indexOf(q) >= 0; })
      .sort(function (a, b) { return (b.label.toLowerCase().indexOf(q) >= 0) - (a.label.toLowerCase().indexOf(q) >= 0); }).slice(0, 40);
    sel = Math.min(sel, Math.max(results.length - 1, 0));
    plist.innerHTML = results.length ? results.map(function (r, i) {
      return '<li><a href="#' + r.href + '" role="option" aria-selected="' + (i === sel) + '"><span>' + r.label + "</span><small>" + r.where + "</small></a></li>";
    }).join("") : '<li class="none">No results. Try another word.</li>';
  }
  var lastFocus = null;
  if (pal && !pal.querySelector(".hint")) {
    var hint = document.createElement("div");
    hint.className = "hint";
    hint.innerHTML = "<span><kbd>↑</kbd><kbd>↓</kbd> navigate</span><span><kbd>↵</kbd> open</span><span><kbd>esc</kbd> close</span>";
    pal.querySelector(".box").appendChild(hint);
  }
  function openPalette() { if (!pal) return; lastFocus = document.activeElement; pal.hidden = false; document.body.style.overflow = "hidden"; pq.value = ""; sel = 0; render(); setTimeout(function () { pq.focus(); }, 0); }
  function closePalette() { if (!pal || pal.hidden) return; pal.hidden = true; document.body.style.overflow = ""; if (lastFocus && lastFocus.focus) { try { lastFocus.focus(); } catch (e) {} } }
  document.querySelectorAll("[data-search]").forEach(function (b) { b.addEventListener("click", openPalette); });
  if (pq) pq.addEventListener("input", function () { sel = 0; render(); });
  if (pal) pal.addEventListener("click", function (e) { if (e.target === pal) closePalette(); });
  document.addEventListener("keydown", function (e) {
    // A page without the palette has nothing to open.
    if (!pal) return;
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") { e.preventDefault(); pal.hidden ? openPalette() : closePalette(); return; }
    if (e.key === "/" && pal.hidden && !/input|textarea/i.test(document.activeElement.tagName)) { e.preventDefault(); openPalette(); return; }
    if (pal.hidden) return;
    if (e.key === "Escape") closePalette();
    else if (e.key === "ArrowDown") { e.preventDefault(); sel = Math.min(sel + 1, results.length - 1); render(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); sel = Math.max(sel - 1, 0); render(); }
    else if (e.key === "Enter" && results[sel]) {
      e.preventDefault();
      try { history.pushState(null, "", "#" + results[sel].href); } catch (err) {}
      show(results[sel].href);
      closePalette();
    }
  });

  // ---- reading experience ----
  order.forEach(function (id) {
    pages[id].querySelectorAll("h2, h3").forEach(function (h, k) {
      if (!h.id) h.id = id + "-" + (k + 1);
      if (h.closest(".section, .hero, .cta-band, .site-footer")) return;
      if (h.querySelector(".anchor")) return;
      var a = document.createElement("a");
      a.className = "anchor";
      a.href = "#" + h.id;
      a.setAttribute("aria-label", "Link to " + h.textContent.trim());
      a.textContent = "#";
      h.appendChild(a);
    });
  });

  var readbar = document.createElement("div");
  readbar.className = "readbar";
  document.body.appendChild(readbar);
  var readTick = false;
  function updateReadbar() {
    readTick = false;
    var d = document.documentElement;
    var max = d.scrollHeight - d.clientHeight;
    readbar.style.width = (max > 0 ? (d.scrollTop / max) * 100 : 0) + "%";
  }
  window.addEventListener("scroll", function () {
    if (!readTick) { readTick = true; requestAnimationFrame(updateReadbar); }
  }, { passive: true });

  var revealEls = Array.prototype.slice.call(document.querySelectorAll(".reveal"));
  if (matchMedia("(prefers-reduced-motion: reduce)").matches || !("IntersectionObserver" in window)) {
    revealEls.forEach(function (el) { el.classList.add("in"); });
  } else {
    var revealObs = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting) { en.target.classList.add("in"); revealObs.unobserve(en.target); }
      });
    }, { rootMargin: "0px 0px -8% 0px", threshold: .08 });
    revealEls.forEach(function (el) { revealObs.observe(el); });
  }

  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-copy]");
    if (!b) return;
    e.preventDefault();
    if (b.hasAttribute("data-label")) copyText(b.getAttribute("data-copy"), b, "copied", "copy");
    else copyText(b.getAttribute("data-copy"), b, ICON.check, ICON.copy);
  });

  // ---- use cases ----
  var ucTabs = Array.prototype.slice.call(document.querySelectorAll(".uc-list [data-uc]"));
  function selectUseCase(tab) {
    ucTabs.forEach(function (t) { t.setAttribute("aria-selected", String(t === tab)); });
    document.querySelectorAll("[data-uc-pane]").forEach(function (p) { p.hidden = p.getAttribute("data-uc-pane") !== tab.getAttribute("data-uc"); });
  }
  ucTabs.forEach(function (t, i) {
    t.addEventListener("click", function () { selectUseCase(t); });
    t.addEventListener("keydown", function (e) {
      var d = e.key === "ArrowDown" || e.key === "ArrowRight" ? 1 : e.key === "ArrowUp" || e.key === "ArrowLeft" ? -1 : 0;
      if (!d) return;
      e.preventDefault();
      var next = ucTabs[(i + d + ucTabs.length) % ucTabs.length];
      selectUseCase(next);
      next.focus();
    });
  });

  // ---- mobile nav ----
  var menubtn = document.getElementById("menubtn"), scrim = document.getElementById("scrim");
  function closeNav() {
    document.body.classList.remove("nav-open");
    if (menubtn) menubtn.setAttribute("aria-expanded", "false");
  }
  if (menubtn) menubtn.addEventListener("click", function () {
    var open = !document.body.classList.contains("nav-open");
    var bar = document.querySelector(".topbar");
    if (bar) document.documentElement.style.setProperty("--drawer-top", Math.max(0, bar.getBoundingClientRect().bottom) + "px");
    document.body.classList.toggle("nav-open", open);
    menubtn.setAttribute("aria-expanded", String(open));
  });
  if (scrim) scrim.addEventListener("click", closeNav);
  document.querySelectorAll(".side a.item").forEach(function (a) { a.addEventListener("click", closeNav); });

  // ---- TOC scrollspy ----
  var tocLinks = Array.prototype.slice.call(document.querySelectorAll("#toc a"));
  if (tocLinks.length && "IntersectionObserver" in window) {
    var map = {};
    tocLinks.forEach(function (a) { map[a.getAttribute("href").slice(1)] = a; });
    var spy = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (!en.isIntersecting) return;
        tocLinks.forEach(function (a) { a.classList.remove("on"); });
        var a = map[en.target.id];
        if (a) a.classList.add("on");
      });
    }, { rootMargin: "-88px 0px -68% 0px", threshold: 0 });
    document.querySelectorAll("section.page h2, section.page h3").forEach(function (h) { if (h.id) spy.observe(h); });
  }

  var totop = document.getElementById("totop");
  if (totop) totop.addEventListener("click", function () { window.scrollTo({ top: 0, behavior: "smooth" }); });

  // ---- mobile table labels (thead is hidden when stacked) ----
  document.querySelectorAll(".table table").forEach(function (table) {
    var heads = Array.prototype.map.call(table.querySelectorAll("thead th"), function (th) {
      return th.textContent.replace(/\s+/g, " ").trim();
    });
    if (!heads.length) return;
    table.querySelectorAll("tbody tr").forEach(function (tr) {
      Array.prototype.forEach.call(tr.children, function (td, i) {
        if (heads[i]) td.setAttribute("data-label", heads[i]);
      });
    });
  });

  // ---- provider / content tabs ----
  document.querySelectorAll(".tabs").forEach(function (root) {
    var tabs = Array.prototype.slice.call(root.querySelectorAll('[role="tab"]'));
    var panels = Array.prototype.slice.call(root.querySelectorAll("[data-panel]"));
    function show(id) {
      tabs.forEach(function (t) {
        var on = t.getAttribute("data-tab") === id;
        t.setAttribute("aria-selected", String(on));
        t.tabIndex = on ? 0 : -1;
      });
      panels.forEach(function (p) { p.hidden = p.getAttribute("data-panel") !== id; });
      try { localStorage.setItem("kritrim-tab:" + (root.id || root.getAttribute("data-tabs") || "default"), id); } catch (e) {}
    }
    var key = "kritrim-tab:" + (root.id || root.getAttribute("data-tabs") || "default");
    var saved = null;
    try { saved = localStorage.getItem(key); } catch (e) {}
    var initial = (saved && tabs.some(function (t) { return t.getAttribute("data-tab") === saved; }))
      ? saved
      : (tabs[0] && tabs[0].getAttribute("data-tab"));
    if (initial) show(initial);
    tabs.forEach(function (t, i) {
      t.addEventListener("click", function () { show(t.getAttribute("data-tab")); });
      t.addEventListener("keydown", function (e) {
        var d = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1
          : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
        if (!d) return;
        e.preventDefault();
        var next = tabs[(i + d + tabs.length) % tabs.length];
        show(next.getAttribute("data-tab"));
        next.focus();
      });
    });
  });
})();
