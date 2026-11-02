/* learnings/guide — search, filters, scrollspy, diagrams, shortcuts */
(() => {
  "use strict";

  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));
  const topOffset = () =>
    document.querySelector(".topbar").getBoundingClientRect().height + 24;

  /* ---------------- theme ---------------- */

  const themeToggle = $("#theme-toggle");
  const applyTheme = (t) => {
    document.documentElement.dataset.theme = t;
    try { localStorage.setItem("guide-theme", t); } catch (_) {}
  };
  themeToggle.addEventListener("click", () => {
    applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark");
  });

  /* ---------------- document index ---------------- */

  const cleanHeading = (h) => {
    const c = h.cloneNode(true);
    const a = c.querySelector(".heading-anchor");
    if (a) a.remove();
    return c.textContent.replace(/\s+/g, " ").trim();
  };

  const docs = $$(".doc").map((el) => ({
    el,
    id: el.id,
    title: el.dataset.title || "",
    category: el.dataset.category || "",
    file: el.dataset.path || "",
    headings: $$("h2, h3, h4", el).map((h) => ({
      el: h,
      id: h.id,
      text: cleanHeading(h),
      level: Number(h.tagName[1]),
    })),
  }));

  const docById = new Map(docs.map((d) => [d.id, d]));

  /* ---------------- search palette ---------------- */

  const search = $("#search");
  const palette = $("#palette");
  let results = [];
  let active = -1;

  const escapeHTML = (s) =>
    s.replace(/[&<>"]/g, (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

  const markMatch = (text, terms) => {
    let out = escapeHTML(text);
    for (const t of terms) {
      if (!t) continue;
      const i = out.toLowerCase().indexOf(t);
      if (i < 0) continue;
      out =
        out.slice(0, i) + "<mark>" + out.slice(i, i + t.length) + "</mark>" + out.slice(i + t.length);
    }
    return out;
  };

  const score = (hay, terms) => {
    let total = 0;
    for (const t of terms) {
      const i = hay.indexOf(t);
      if (i < 0) return -1;
      total += i === 0 ? 60 : hay.slice(Math.max(0, i - 1)).startsWith(" " + t) ? 40 : 15;
    }
    return total;
  };

  const runSearch = (q) => {
    const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
    if (!terms.length) return [];
    const hits = [];
    for (const d of docs) {
      const dHay = (d.title + " " + d.category + " " + d.file).toLowerCase();
      const dScore = score(dHay, terms);
      if (dScore >= 0) {
        hits.push({ id: d.id, title: d.title, doc: d.file, cat: d.category, score: dScore + 80 });
      }
      for (const h of d.headings) {
        const s = score((h.text + " " + d.title).toLowerCase(), terms);
        if (s < 0) continue;
        hits.push({
          id: h.id,
          title: h.text,
          doc: d.title,
          cat: d.category,
          score: s + (h.level === 2 ? 30 : h.level === 3 ? 18 : 8),
        });
      }
    }
    hits.sort((a, b) => b.score - a.score);
    return hits.slice(0, 40);
  };

  const renderPalette = () => {
    const q = search.value.trim();
    if (!q) {
      palette.hidden = true;
      search.setAttribute("aria-expanded", "false");
      return;
    }
    results = runSearch(q);
    active = results.length ? 0 : -1;
    if (!results.length) {
      palette.innerHTML = '<div class="palette-empty">No matches for "' + escapeHTML(q) + '"</div>';
    } else {
      const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
      palette.innerHTML = results
        .map(
          (r, i) =>
            '<button type="button" class="palette-item' +
            (i === active ? " is-active" : "") +
            '" role="option" data-target="' + escapeHTML(r.id) + '" data-i="' + i + '">' +
            '<span class="p-title">' + markMatch(r.title, terms) + "</span>" +
            '<span class="p-doc">' + escapeHTML(r.cat) + " · " + escapeHTML(r.doc) + "</span>" +
            "</button>"
        )
        .join("");
    }
    palette.hidden = false;
    search.setAttribute("aria-expanded", "true");
  };

  const closePalette = () => {
    palette.hidden = true;
    search.setAttribute("aria-expanded", "false");
    active = -1;
  };

  const moveActive = (delta) => {
    if (!results.length) return;
    active = (active + delta + results.length) % results.length;
    $$(".palette-item", palette).forEach((el, i) => {
      el.classList.toggle("is-active", i === active);
      if (i === active) el.scrollIntoView({ block: "nearest" });
    });
  };

  const goTo = (id) => {
    closePalette();
    search.blur();
    const target = document.getElementById(id);
    if (!target) return;
    location.hash = "#" + id;
    if (mobileNavOpen()) closeNav();
  };

  search.addEventListener("input", () => {
    renderPalette();
    filterSidebar(search.value.trim());
  });

  search.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); moveActive(1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); moveActive(-1); }
    else if (e.key === "Enter") {
      if (active >= 0 && results[active]) { e.preventDefault(); goTo(results[active].id); }
    } else if (e.key === "Escape") {
      if (search.value) {
        search.value = "";
        renderPalette();
        filterSidebar("");
      } else {
        closePalette();
        search.blur();
      }
    }
  });

  palette.addEventListener("mousedown", (e) => {
    const item = e.target.closest(".palette-item");
    if (!item) return;
    e.preventDefault();
    goTo(item.dataset.target);
  });

  search.addEventListener("blur", () => setTimeout(closePalette, 120));

  /* ---------------- category chips ---------------- */

  const chips = $$("#chips .chip");
  const clearFilter = $("#clear-filter");
  const filterMeta = $("#filter-meta");
  const filterCount = $("#filter-count");
  let activeCat = "*";

  const applyFilter = (cat) => {
    activeCat = cat;
    chips.forEach((c) => c.classList.toggle("is-active", c.dataset.cat === cat));
    filterMeta.hidden = cat === "*";
    if (cat !== "*") {
      const n = docs.filter((d) => d.category === cat).length;
      filterCount.textContent = n + " of " + docs.length + " sections";
      const act = chips.find((c) => c.dataset.cat === cat);
      if (act) act.scrollIntoView({ inline: "center", block: "nearest" });
    }
    docs.forEach((d) => d.el.classList.toggle("is-filtered-out", cat !== "*" && d.category !== cat));
    $$(".nav-group").forEach((g) =>
      g.classList.toggle("is-filtered-out", cat !== "*" && g.dataset.category !== cat)
    );
    const cur = currentDoc();
    if (cur && cat !== "*" && cur.category !== cat) {
      const first = docs.find((d) => cat === "*" || d.category === cat);
      if (first) setActiveDoc(first);
      else clearActive();
    }
    rebuildHeadings();
    refresh();
  };

  chips.forEach((c) => c.addEventListener("click", () => applyFilter(c.dataset.cat)));
  clearFilter.addEventListener("click", () => applyFilter("*"));

  /* ---------------- sidebar filter (search) ---------------- */

  const navDocs = $$(".nav-doc");

  const filterSidebar = (q) => {
    const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
    navDocs.forEach((nd) => {
      if (!terms.length) {
        nd.classList.remove("is-filtered-out");
        return;
      }
      const hay = nd.textContent.toLowerCase();
      nd.classList.toggle("is-filtered-out", !terms.every((t) => hay.includes(t)));
    });
    $$(".nav-group").forEach((g) => {
      if (!terms.length) { g.classList.remove("is-filtered-out"); return; }
      const visible = $$(".nav-doc:not(.is-filtered-out)", g).length > 0;
      g.classList.toggle("is-filtered-out", !visible);
    });
  };

  /* ---------------- nav groups & mobile drawer ---------------- */

  $$(".nav-group-head").forEach((btn) =>
    btn.addEventListener("click", () => {
      const g = btn.closest(".nav-group");
      if (g.classList.contains("is-leaf")) return;
      const collapsed = g.classList.toggle("is-collapsed");
      btn.setAttribute("aria-expanded", String(!collapsed));
    })
  );

  const sidebar = $("#sidebar");
  const backdrop = $("#backdrop");
  const navToggle = $("#nav-toggle");
  const mobileNavOpen = () => sidebar.classList.contains("is-open");

  const openNav = () => {
    sidebar.classList.add("is-open");
    backdrop.hidden = false;
    navToggle.setAttribute("aria-expanded", "true");
  };
  const closeNav = () => {
    sidebar.classList.remove("is-open");
    backdrop.hidden = true;
    navToggle.setAttribute("aria-expanded", "false");
  };
  navToggle.addEventListener("click", () => (mobileNavOpen() ? closeNav() : openNav()));
  backdrop.addEventListener("click", closeNav);

  sidebar.addEventListener("click", (e) => {
    if (e.target.closest("a") && mobileNavOpen()) closeNav();
  });

  /* ---------------- scrollspy: active doc + heading ---------------- */

  let headings = [];
  let visibleDocs = docs.slice();

  const rebuildHeadings = () => {
    visibleDocs = docs.filter(
      (d) => !d.el.classList.contains("is-filtered-out")
    );
    headings = [];
    for (const d of visibleDocs) {
      for (const h of d.headings) headings.push({ doc: d, ...h });
    }
  };

  const tocBody = $("#toc-body");
  let activeDocId = null;
  let activeHeadingId = null;

  const currentDoc = () => docs.find((d) => d.id === activeDocId) || null;

  const clearActive = () => {
    activeDocId = null;
    activeHeadingId = null;
    $$(".nav-doc").forEach((n) => n.classList.remove("is-current"));
    tocBody.innerHTML = "";
  };

  const setActiveDoc = (d) => {
    if (activeDocId === d.id) return;
    activeDocId = d.id;
    lastDocId = d.id;
    try { localStorage.setItem("guide-last-doc", d.id); } catch (_) {}
    navDocs.forEach((n) => n.classList.toggle("is-current", n.dataset.doc === d.id));
    const nd = navDocs.find((n) => n.dataset.doc === d.id);
    if (nd) {
      const group = nd.closest(".nav-group");
      if (group && group.classList.contains("is-collapsed")) {
        group.classList.remove("is-collapsed");
        group.querySelector(".nav-group-head").setAttribute("aria-expanded", "true");
      }
      nd.scrollIntoView({ block: "nearest" });
    }
    tocBody.innerHTML = d.headings
      .map(
        (h) =>
          '<a href="#' + h.id + '" data-heading="' + h.id + '" class="lvl-' +
          Math.min(h.level, 4) + '">' + escapeHTML(h.text) + "</a>"
      )
      .join("");
    activeHeadingId = null;
  };

  const setActiveHeading = (id) => {
    if (activeHeadingId === id) return;
    activeHeadingId = id;
    $$(".toc-body a").forEach((a) => a.classList.toggle("is-active", a.dataset.heading === id));
  };

  const progress = $("#progress");

  const refresh = () => {
    const docH = document.documentElement.scrollHeight - window.innerHeight;
    progress.style.width = (docH > 0 ? (window.scrollY / docH) * 100 : 0) + "%";

    const off = topOffset();
    let doc = null;
    for (const d of visibleDocs) {
      if (d.el.getBoundingClientRect().top <= off) doc = d;
    }
    if (!doc && visibleDocs.length) doc = visibleDocs[0];
    if (!doc) { clearActive(); return; }
    setActiveDoc(doc);

    let cur = null;
    for (const h of headings) {
      if (h.doc.id !== doc.id) continue;
      if (h.el.getBoundingClientRect().top <= off + 8) cur = h;
    }
    setActiveHeading(cur ? cur.id : null);
  };

  let ticking = false;
  const onScroll = () => {
    if (ticking) return;
    ticking = true;
    requestAnimationFrame(() => { refresh(); ticking = false; });
  };
  window.addEventListener("scroll", onScroll, { passive: true });
  window.addEventListener("resize", onScroll);

  /* ---------------- copy buttons ---------------- */

  const toast = $("#toast");
  let toastTimer = 0;
  const showToast = (msg) => {
    toast.textContent = msg;
    toast.classList.add("is-visible");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toast.classList.remove("is-visible"), 1600);
  };

  /* ---------------- learning progress ---------------- */

  const progressKey = "guide-completed-docs";
  const readCompletedDocs = () => {
    try {
      const stored = JSON.parse(localStorage.getItem(progressKey) || "[]");
      return new Set(Array.isArray(stored) ? stored.filter((id) => docById.has(id)) : []);
    } catch (_) { return new Set(); }
  };
  const completedDocs = readCompletedDocs();
  const progressPanel = $("#progress-panel");
  const progressToggle = $("#progress-toggle");
  const progressList = $("#progress-list");
  const progressResumeTitle = $("#progress-resume-title");
  let lastDocId = "";
  try {
    const saved = localStorage.getItem("guide-last-doc") || "";
    if (docById.has(saved)) lastDocId = saved;
  } catch (_) {}

  const saveCompletedDocs = () => {
    try { localStorage.setItem(progressKey, JSON.stringify(Array.from(completedDocs))); } catch (_) {}
  };

  const addCompletionControls = () => {
    for (const d of docs) {
      const head = $(".doc-head", d.el);
      if (head && !$(".doc-complete", head)) {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "doc-complete";
        button.addEventListener("click", () => toggleComplete(d.id));
        const permalink = $(".doc-permalink", head);
        head.insertBefore(button, permalink || null);
        d.completeButton = button;
      }

      const nav = navDocs.find((item) => item.dataset.doc === d.id);
      const title = nav && $(".nav-doc-title", nav);
      if (title && !$(".nav-doc-state", title)) {
        const state = document.createElement("span");
        state.className = "nav-doc-state";
        state.setAttribute("aria-hidden", "true");
        state.textContent = "✓";
        title.appendChild(state);
      }
    }
  };

  const renderProgress = () => {
    const total = docs.length;
    const count = completedDocs.size;
    const percent = total ? Math.round((count / total) * 100) : 0;
    $("#progress-mini").textContent = count + "/" + total;
    progressToggle.setAttribute("aria-label", "Learning progress: " + count + " of " + total + " guides complete");
    $("#progress-summary").textContent = count + " of " + total + " guides complete";
    $("#progress-percent").textContent = percent + "%";
    $("#learning-progress-bar").style.width = percent + "%";

    const resumeDoc = (lastDocId && docById.get(lastDocId)) || docs.find((d) => !completedDocs.has(d.id)) || docs[0];
    if (resumeDoc) {
      progressResumeTitle.textContent = completedDocs.size === total
        ? "Review " + resumeDoc.title
        : resumeDoc.title;
      $("#progress-resume").dataset.target = resumeDoc.id;
    }

    progressList.innerHTML = docs.map((d) => {
      const done = completedDocs.has(d.id);
      return '<div class="progress-item' + (done ? " is-complete" : "") + '" data-progress-doc="' + escapeHTML(d.id) + '">' +
        '<button type="button" class="progress-jump" data-progress-jump="' + escapeHTML(d.id) + '">' +
        '<b>' + escapeHTML(d.title) + '</b><small>' + escapeHTML(d.category) + ' · ' + escapeHTML(d.file) + '</small></button>' +
        '<button type="button" class="progress-check" data-progress-check="' + escapeHTML(d.id) + '" aria-pressed="' + String(done) + '">' +
        (done ? "✓ Complete" : "Mark read") + '</button></div>';
    }).join("");

    for (const d of docs) {
      const done = completedDocs.has(d.id);
      if (d.completeButton) {
        d.completeButton.setAttribute("aria-pressed", String(done));
        d.completeButton.setAttribute("aria-label", (done ? "Mark as unread: " : "Mark as read: ") + d.title);
        d.completeButton.innerHTML = '<span class="doc-complete-mark" aria-hidden="true">' + (done ? "✓" : "○") + '</span>' + (done ? "Read" : "Mark read");
      }
      const nav = navDocs.find((item) => item.dataset.doc === d.id);
      if (nav) nav.classList.toggle("is-completed", done);
    }
  };

  const toggleComplete = (id) => {
    if (!docById.has(id)) return;
    if (completedDocs.has(id)) completedDocs.delete(id);
    else completedDocs.add(id);
    saveCompletedDocs();
    renderProgress();
    showToast(completedDocs.has(id) ? "guide marked complete" : "completion removed");
  };

  const closeProgress = (restoreFocus = true) => {
    progressPanel.hidden = true;
    if (restoreFocus) progressToggle.focus();
  };
  progressToggle.addEventListener("click", () => {
    renderProgress();
    progressPanel.hidden = false;
    $("#progress-close").focus();
  });
  $("#progress-close").addEventListener("click", () => closeProgress());
  progressPanel.addEventListener("click", (event) => {
    if (event.target === progressPanel) { closeProgress(); return; }
    const check = event.target.closest("[data-progress-check]");
    if (check) { toggleComplete(check.dataset.progressCheck); return; }
    const jump = event.target.closest("[data-progress-jump]");
    if (jump) {
      const target = jump.dataset.progressJump;
      closeProgress(false);
      if (activeCat !== "*") applyFilter("*");
      goTo(target);
    }
  });
  progressPanel.addEventListener("keydown", (event) => {
    if (event.key !== "Tab") return;
    const focusable = $$('button:not([disabled]), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])', progressPanel)
      .filter((el) => el.getClientRects().length > 0);
    if (!focusable.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  });
  $("#progress-resume").addEventListener("click", (event) => {
    const target = event.currentTarget.dataset.target;
    closeProgress(false);
    if (activeCat !== "*") applyFilter("*");
    goTo(target);
  });
  $("#progress-reset").addEventListener("click", () => {
    if (!completedDocs.size || !window.confirm("Reset all guide progress on this device?")) return;
    completedDocs.clear();
    saveCompletedDocs();
    renderProgress();
    showToast("learning progress reset");
  });
  addCompletionControls();
  renderProgress();

  $$("pre.code").forEach((pre) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "copy-btn";
    btn.textContent = "copy";
    btn.setAttribute("aria-label", "Copy this code");
    btn.addEventListener("click", async () => {
      const text = pre.querySelector("code") ? pre.querySelector("code").textContent : pre.textContent;
      try {
        await navigator.clipboard.writeText(text);
        btn.textContent = "copied ✓";
        btn.classList.add("is-done");
      } catch (_) {
        const ta = document.createElement("textarea");
        ta.value = text;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        ta.remove();
        btn.textContent = "copied ✓";
        btn.classList.add("is-done");
      }
      setTimeout(() => { btn.textContent = "copy"; btn.classList.remove("is-done"); }, 1500);
    });
    pre.appendChild(btn);
  });

  /* ---------------- interactive diagrams ---------------- */

  $$("[data-diagram]").forEach((fig) => {
    const status = $("[data-diagram-status]", fig);
    const traceBtn = $("[data-trace]", fig);
    const nodes = $$(".diagram-node", fig);
    const def = "click a stage to pin it";
    let timer = 0;
    let running = false;

    const stop = (msg) => {
      running = false;
      clearInterval(timer);
      fig.classList.remove("is-tracing");
      nodes.forEach((n) => n.classList.remove("is-live"));
      $$(".diagram-arrow", fig).forEach((a) => a.classList.remove("is-lit"));
      traceBtn.classList.remove("is-running");
      traceBtn.textContent = "▶ trace flow";
      status.textContent = msg || def;
    };

    const trace = () => {
      if (running) { stop("stopped"); return; }
      if (!nodes.length) return;
      fig.classList.remove("has-pin");
      nodes.forEach((n) => n.classList.remove("is-pinned"));
      running = true;
      traceBtn.classList.add("is-running");
      traceBtn.textContent = "■ stop";
      fig.classList.add("is-tracing");
      let i = 0;
      const step = () => {
        nodes.forEach((n) => n.classList.remove("is-live"));
        $$(".diagram-arrow", fig).forEach((a) => a.classList.remove("is-lit"));
        const node = nodes[i];
        node.classList.add("is-live");
        const prev = node.previousElementSibling;
        if (prev && prev.classList.contains("diagram-arrow")) prev.classList.add("is-lit");
        const note = node.querySelector(".node-note");
        status.textContent =
          "step " + (i + 1) + "/" + nodes.length + " · " +
          node.querySelector(".node-main").textContent +
          (note ? " — " + note.textContent : "");
        i += 1;
        if (i >= nodes.length) {
          clearInterval(timer);
          setTimeout(() => stop("done · " + nodes.length + " stages"), 700);
        }
      };
      step();
      timer = setInterval(step, 850);
    };

    traceBtn.addEventListener("click", trace);

    nodes.forEach((node) => {
      const describe = () => {
        if (running) return;
        const main = node.querySelector(".node-main").textContent;
        const note = node.querySelector(".node-note");
        status.textContent = main + (note ? " — " + note.textContent : "");
      };
      node.addEventListener("mouseenter", describe);
      node.addEventListener("focus", describe);
      node.addEventListener("mouseleave", () => { if (!running) status.textContent = def; });
      node.addEventListener("click", () => {
        const wasPinned = node.classList.contains("is-pinned");
        nodes.forEach((n) => n.classList.remove("is-pinned"));
        fig.classList.toggle("has-pin", !wasPinned);
        if (!wasPinned) {
          node.classList.add("is-pinned");
          describe();
        } else {
          status.textContent = def;
        }
      });
    });
  });

  /* ---------------- help overlay ---------------- */

  const help = $("#help");
  const setHelp = (open) => { help.hidden = !open; };
  $("#help-toggle").addEventListener("click", () => setHelp(help.hidden));
  $("#help-close").addEventListener("click", () => setHelp(false));
  help.addEventListener("click", (e) => { if (e.target === help) setHelp(false); });

  /* ---------------- listen player (speech synthesis) ---------------- */

  const narrateSupported = "speechSynthesis" in window && "SpeechSynthesisUtterance" in window;
  const player = $("#player");
  const pPlay = $("#p-play");
  const pTitle = $("#player-title");
  const pCount = $("#player-count");
  const pSpeed = $("#p-speed");
  const voiceSel = $("#p-voice");
  const voiceRow = $("#p-voice-row");
  const listenToggle = $("#listen-toggle");

  const rates = [1, 1.25, 1.5, 0.75];
  let rateI = 0;
  let units = [];
  let idx = 0;
  let narrating = false;
  let paused = false;
  let token = 0;
  let speakTimer = 0;
  let voiceList = [];

  const say = (msg) => showToast(msg);

  /* cloud/Natural voices first, known-robotic ones blacklisted */
  const rankVoice = (v) => {
    const n = v.name.toLowerCase();
    let s = 0;
    if (/natural|neural|premium|enhanced|online|cloud|wavenet|studio|hd/.test(n)) s += 100;
    if (/google/.test(n)) s += 60;
    if (/samantha|daniel|alex|aria|jenny|guy|zira|david|libby|sonia|ava|emma|brian|andrew|michelle|amber|amanda|joanna|salli|ivy|kendra|kimberly|matthew|ryan|erin|joey/.test(n)) s += 30;
    if (/espeak|e-speak|festival|flite|pico|robot|compact|dummy|lite|basic|unison/.test(n)) s -= 300;
    if (v.localService === false) s += 40;
    if (/^en-US/i.test(v.lang)) s += 20;
    else if (/^en-/i.test(v.lang)) s += 10;
    if (/^en/i.test(v.lang)) s += 5;
    else s -= 500;
    return s;
  };

  const robotic = (v) =>
    !!v && /espeak|e-speak|festival|flite|pico|robot|compact|dummy|lite|basic/i.test(v.name);

  const voiceId = (v) => v.name + " @ " + v.lang;
  const savedVoice = () => {
    try { return localStorage.getItem("guide-voice") || ""; } catch (_) { return ""; }
  };

  const populateVoices = () => {
    if (!narrateSupported) return;
    voiceList = speechSynthesis.getVoices();
    if (!voiceList.length) return;
    const ranked = voiceList.slice().sort((a, b) => rankVoice(b) - rankVoice(a));
    const pool = ranked.filter((v) => /^en/i.test(v.lang));
    const use = (pool.length ? pool : ranked).slice(0, 40);
    const prev = voiceSel.value || savedVoice();
    voiceSel.innerHTML = use
      .map((v) => '<option value="' + escapeHTML(voiceId(v)) + '">' + escapeHTML(v.name + " · " + v.lang) + "</option>")
      .join("");
    if (prev && use.some((v) => voiceId(v) === prev)) voiceSel.value = prev;
    voiceRow.hidden = !use.length;
  };

  const currentVoice = () => {
    if (!voiceList.length) populateVoices();
    if (!voiceList.length) return null;
    return (
      voiceList.find((v) => voiceId(v) === voiceSel.value) ||
      voiceList.slice().sort((a, b) => rankVoice(b) - rankVoice(a))[0] ||
      null
    );
  };

  if (narrateSupported) {
    speechSynthesis.addEventListener("voiceschanged", populateVoices);
    populateVoices();
    setTimeout(populateVoices, 800); // some engines report voices late
    document.addEventListener("pointerdown", () => populateVoices(), { once: true });
  }

  const announceVoice = () => {
    const v = currentVoice();
    if (!v) return;
    if (robotic(v)) {
      say("only " + v.name + " is installed — try Chrome or Edge, which load natural online voices");
    } else {
      say("reading with " + v.name);
    }
  };

  voiceSel.addEventListener("change", () => {
    try { localStorage.setItem("guide-voice", voiceSel.value); } catch (_) {}
    const v = currentVoice();
    if (v) say(v.name);
    if (narrating && !paused) speak();
  });

  $("#p-voice-test").addEventListener("click", () => {
    if (!narrateSupported) return;
    const v = currentVoice();
    if (!v) { say("no voices available"); return; }
    const tok = ++token;
    clearTimeout(speakTimer);
    speechSynthesis.cancel();
    const resumeAfter = narrating && !paused;
    const utt = new SpeechSynthesisUtterance(
      "Hello. This is " + v.name + ", and this is how the guide will sound."
    );
    utt.rate = rates[rateI];
    utt.voice = v;
    utt.lang = v.lang;
    utt.onend = () => {
      if (tok !== token || !resumeAfter || !narrating || paused) return;
      speak();
    };
    setTimeout(() => {
      if (tok !== token) return;
      speechSynthesis.speak(utt);
    }, 60);
  });

  const splitSentences = (text) => {
    const parts = text.split(/(?<=[.!?…])\s+/).filter((s) => s.trim());
    return parts.length ? parts : [text];
  };

  const buildUnits = (d) => {
    $$(".is-speaking").forEach((el) => el.classList.remove("is-speaking"));
    const out = [];
    d.el.querySelectorAll("h2, h3, h4, p, li, blockquote").forEach((el) => {
      if (el.closest("pre, table, figure, .source-panel, nav")) return;
      const clone = el.cloneNode(true);
      clone.querySelectorAll(".heading-anchor, .copy-btn").forEach((n) => n.remove());
      const text = clone.textContent.replace(/\s+/g, " ").trim();
      if (!text) return;
      if (el.tagName === "LI") {
        const strip = clone.cloneNode(true);
        strip.querySelectorAll("a").forEach((n) => n.remove());
        if (!strip.textContent.replace(/\s+/g, " ").trim()) return;
      }
      const pieces = /^H[234]$/.test(el.tagName) ? [text] : splitSentences(text);
      pieces.forEach((s) => out.push({ text: s, el, doc: d }));
    });
    return out;
  };

  const updateMeta = () => {
    const u = units[Math.min(idx, Math.max(units.length - 1, 0))];
    pTitle.textContent = u ? u.doc.title : "";
    pCount.textContent = units.length ? idx + 1 + "/" + units.length : "";
  };

  const setPlayGlyph = () => {
    const showPause = narrating && !paused;
    pPlay.textContent = showPause ? "⏸" : "▶";
    pPlay.setAttribute("aria-label", showPause ? "Pause" : "Play");
    listenToggle.textContent = narrating ? pPlay.textContent : "♪";
    listenToggle.setAttribute("aria-pressed", String(narrating));
  };

  const speak = () => {
    const u = units[idx];
    if (!u || !narrateSupported) return;
    const tok = ++token;
    $$(".is-speaking").forEach((el) => el.classList.remove("is-speaking"));
    u.el.classList.add("is-speaking");
    updateMeta();
    const r = u.el.getBoundingClientRect();
    const off = topOffset();
    if (r.top < off || r.bottom > window.innerHeight - 16) {
      u.el.scrollIntoView({ block: "center", behavior: "smooth" });
    }
    speechSynthesis.cancel();
    const utt = new SpeechSynthesisUtterance(u.text);
    utt.rate = rates[rateI];
    const v = currentVoice();
    if (v) { utt.voice = v; utt.lang = v.lang; }
    utt.onend = () => {
      if (tok !== token || !narrating || paused) return;
      advance(1);
    };
    clearTimeout(speakTimer);
    speakTimer = setTimeout(() => {
      if (tok !== token || !narrating || paused) return;
      speechSynthesis.speak(utt);
    }, 60);
  };

  const loadDoc = (d) => {
    units = buildUnits(d);
    idx = 0;
    updateMeta();
  };

  const stopNarration = () => {
    token++;
    clearTimeout(speakTimer);
    if (narrateSupported) speechSynthesis.cancel();
    narrating = false;
    paused = false;
    $$(".is-speaking").forEach((el) => el.classList.remove("is-speaking"));
    player.hidden = true;
    setPlayGlyph();
  };

  const advance = (delta) => {
    const next = idx + delta;
    if (next < 0) { idx = 0; speak(); return; }
    if (next >= units.length) {
      const cur = units.length ? units[units.length - 1].doc : null;
      const i = cur ? visibleDocs.findIndex((d) => d.id === cur.id) : -1;
      const nd = visibleDocs[i + 1];
      if (nd) {
        location.hash = "#" + nd.id;
        loadDoc(nd);
        if (units.length) { speak(); return; }
      }
      stopNarration();
      say("narration finished");
      return;
    }
    idx = next;
    speak();
  };

  const startOrToggle = () => {
    if (!narrateSupported) { say("speech synthesis unavailable in this browser"); return; }
    if (!narrating) {
      const cur = currentDoc() || visibleDocs[0] || docs[0];
      if (!cur) return;
      narrating = true;
      paused = false;
      player.hidden = false;
      setPlayGlyph();
      loadDoc(cur);
      if (!units.length) { say("nothing to read in this document"); stopNarration(); return; }
      speak();
      announceVoice();
      return;
    }
    if (paused) {
      paused = false;
      setPlayGlyph();
      speak(); // engine-independent restart of the current sentence
    } else {
      paused = true;
      setPlayGlyph();
      token++; // invalidate callbacks of the utterance we are dropping
      clearTimeout(speakTimer);
      if (narrateSupported) speechSynthesis.cancel(); // stops audio on every engine
    }
  };

  const seek = (delta) => {
    if (!narrating) return;
    if (paused) {
      paused = false;
      speechSynthesis.cancel();
    }
    setPlayGlyph();
    advance(delta);
  };

  const jumpNarration = (delta) => {
    if (!narrating) return;
    const cur = units.length ? units[Math.min(idx, units.length - 1)].doc : null;
    let i = cur ? visibleDocs.findIndex((d) => d.id === cur.id) : 0;
    if (i < 0) i = 0;
    const ni = Math.min(Math.max(i + delta, 0), visibleDocs.length - 1);
    const nd = visibleDocs[ni];
    if (!nd || (cur && nd.id === cur.id)) return;
    location.hash = "#" + nd.id;
    paused = false;
    setPlayGlyph();
    loadDoc(nd);
    if (units.length) speak();
  };

  $("#listen-toggle").addEventListener("click", startOrToggle);
  pPlay.addEventListener("click", startOrToggle);
  $("#p-back").addEventListener("click", () => seek(-1));
  $("#p-fwd").addEventListener("click", () => seek(1));
  $("#p-prev").addEventListener("click", () => jumpNarration(-1));
  $("#p-next").addEventListener("click", () => jumpNarration(1));
  $("#p-close").addEventListener("click", stopNarration);
  pSpeed.addEventListener("click", () => {
    rateI = (rateI + 1) % rates.length;
    pSpeed.textContent = rates[rateI] + "×";
    if (narrating && !paused) speak();
  });

  if (narrateSupported) {
    setInterval(() => {
      if (narrating && !paused) speechSynthesis.resume();
    }, 12000);
  }

  /* ---------------- keyboard shortcuts ---------------- */

  const jumpDoc = (delta) => {
    if (!visibleDocs.length) return;
    let i = visibleDocs.findIndex((d) => d.id === activeDocId);
    if (i < 0) i = 0;
    i = Math.min(Math.max(i + delta, 0), visibleDocs.length - 1);
    location.hash = "#" + visibleDocs[i].id;
  };

  document.addEventListener("keydown", (e) => {
    const el = e.target;
    const typing =
      el instanceof HTMLElement &&
      (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable);

    if (e.key === "Escape") {
      if (!help.hidden) { setHelp(false); return; }
      if (!progressPanel.hidden) { closeProgress(); return; }
      if (mobileNavOpen()) { closeNav(); return; }
      if (typing) { el.blur(); return; }
      if (narrating) { stopNarration(); return; }
      return;
    }
    if (!progressPanel.hidden) return;
    if (typing || e.metaKey || e.ctrlKey || e.altKey) return;

    if (e.key === "/") { e.preventDefault(); search.focus(); search.select(); }
    else if (e.key === "?") { e.preventDefault(); setHelp(help.hidden); }
    else if (e.key === "p") {
      e.preventDefault();
      renderProgress();
      progressPanel.hidden = false;
      $("#progress-close").focus();
    }
    else if (e.key === "t") {
      applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark");
    }
    else if (e.key === "j") { e.preventDefault(); jumpDoc(1); }
    else if (e.key === "k") { e.preventDefault(); jumpDoc(-1); }
    else if (e.key === "s") { e.preventDefault(); startOrToggle(); }
    else if (e.key === " " && narrating) { e.preventDefault(); startOrToggle(); }
    else if (e.key === ",") { e.preventDefault(); seek(-1); }
    else if (e.key === ".") { e.preventDefault(); seek(1); }
  });

  /* ---------------- init ---------------- */

  rebuildHeadings();

  window.addEventListener("hashchange", () => refresh());
  refresh();
})();
