/* The page's whole logic: fetch, then write text into nodes that already exist.
 *
 * Three rules hold it to what CLAUDE.md asks of this repository, and each one is
 * mechanically checked by a Go test rather than left as an intention here.
 *
 *  - Every value reaches the document through textContent. Never through markup
 *    built from a string: the emulator answers with names a client chose, and a
 *    page that concatenated them into HTML would be the same defect cloudinit
 *    already paid for in YAML, one layer up.
 *  - No provider is named anywhere in this file. Providers, products,
 *    operations and paths all arrive as data, so a fourth pack appears here
 *    without an edit.
 *  - Nothing is ever sent. Every request below is a GET, and the log arrives on
 *    a one-way event stream, so there is no path from this page to a command.
 *
 * And the reason it repaints so carefully: a dashboard that rewrites its numbers
 * every two seconds flickers, and a flickering dashboard gets closed. setText
 * and setWidth write only when the value actually changed.
 */

(function () {
  "use strict";

  var REFRESH_MS = 2000;
  var DATA_REFRESH_MS = 30000;

  function byId(id) { return document.getElementById(id); }

  /* Writes only on change, so the browser has nothing to repaint when the
     emulator answers the same numbers it answered two seconds ago. */
  function setText(node, value) {
    if (!node) { return; }
    var text = String(value);
    if (node.textContent !== text) { node.textContent = text; }
  }

  function setWidth(node, percent) {
    if (!node) { return; }
    var width = percent.toFixed(3) + "%";
    if (node.style.width !== width) { node.style.width = width; }
  }

  function plural(n, one, many) { return n === 1 ? one : many; }

  /* ---- theme ------------------------------------------------------------ */

  /* Three states rather than two: a reader who has expressed no preference must
     keep following the system when it flips at sunset, which "light or dark" as
     a boolean cannot do. */
  var THEMES = ["auto", "light", "dark"];
  var themeButton = byId("theme");

  function applyTheme(name) {
    document.documentElement.setAttribute("data-theme", name);
    setText(themeButton, "theme: " + name);
    try { window.localStorage.setItem("feint.theme", name); } catch (e) { /* private mode */ }
  }

  function initTheme() {
    var stored = null;
    try { stored = window.localStorage.getItem("feint.theme"); } catch (e) { /* private mode */ }
    applyTheme(THEMES.indexOf(stored) >= 0 ? stored : "auto");
    themeButton.addEventListener("click", function () {
      var current = document.documentElement.getAttribute("data-theme");
      var next = THEMES[(THEMES.indexOf(current) + 1) % THEMES.length];
      applyTheme(next);
    });
  }

  /* ---- fetching --------------------------------------------------------- */

  function getJSON(path) {
    return fetch(path, { cache: "no-store", credentials: "omit" }).then(function (resp) {
      if (!resp.ok) { throw new Error(path + " answered " + resp.status); }
      return resp.json();
    });
  }

  var health = byId("health");
  var healthText = byId("health-text");

  /* Reachability is shown, never inferred into the numbers. When the emulator
     stops answering the figures stay as they were, dimmed by the header state:
     replacing them with zeroes would read as an emulator nobody has used, which
     is a different fact entirely. */
  function setReachable(up, reason) {
    health.setAttribute("data-state", up ? "up" : "down");
    setText(healthText, up ? "up" : reason || "unreachable");
  }

  /* ---- the header ------------------------------------------------------- */

  function renderHealth(data) {
    setText(byId("driver"), data.machines || "none");
    setText(byId("resources"), data.resources);
  }

  /* ---- served, driven, probed ------------------------------------------- */

  function renderConformance(data) {
    var served = data.served || 0;
    var driven = data.exercised || 0;
    var probed = data.probed || 0;
    var unproven = Math.max(served - driven - probed, 0);

    setText(byId("served"), served);
    setText(byId("n-driven"), driven);
    setText(byId("n-probed"), probed);
    setText(byId("n-unproven"), unproven);

    /* One bar, never a sum: the hatched part is the routes whose protocol holds
       and whose behaviour nobody has proven. An emulator answering a well-formed
       empty object would pass every probe, which is why it is not filled. */
    var scale = served > 0 ? 100 / served : 0;
    setWidth(byId("seg-driven"), driven * scale);
    setWidth(byId("seg-probed"), probed * scale);
    byId("meter").setAttribute("aria-label",
      driven + " of " + served + " routes driven by a real client, " +
      probed + " probed only, " + unproven + " never proven");

    var contracts = data.contracts || [];
    if (contracts.length === 0) {
      setText(byId("contracts-note"),
        "No contract loaded: no response is being checked against a schema this session. " +
        "Restart with --contracts to turn that on.");
    } else {
      setText(byId("contracts-note"),
        "Responses checked against " + contracts.length + " API " +
        plural(contracts.length, "description", "descriptions") + ": " + contracts.join(", ") + ".");
    }

    /* Two counts that are already computed and that nothing else surfaces. The
       second one is the causal defect this project cares about most: a field the
       client sent and no handler read is an argument the API accepted and then
       ignored. */
    var violations = Object.keys(data.violations || {}).length;
    var unread = Object.keys(data.unread_request_fields || {}).length;
    var problems = [];
    if (violations > 0) {
      problems.push(violations + " " + plural(violations, "operation", "operations") +
        " answered something its API description does not define");
    }
    if (unread > 0) {
      problems.push(unread + " " + plural(unread, "operation", "operations") +
        " received a field no handler read");
    }
    var note = byId("violations-note");
    if (problems.length === 0) {
      note.hidden = true;
    } else {
      note.hidden = false;
      setText(note, problems.join("; ") + ".");
    }
  }

  /* ---- the upstream gap ------------------------------------------------- */

  var upstreamRows = byId("upstream-rows");
  var upstreamNodes = Object.create(null);

  /* One row per upstream product, built once and updated in place. The key is
     the provider's own name joined to the product's, both of them data. */
  function upstreamRow(key, item) {
    var row = upstreamNodes[key];
    if (row) { return row; }

    var el = document.createElement("div");
    el.className = "row";

    var name = document.createElement("span");
    name.className = "name";
    var provider = document.createElement("span");
    provider.className = "provider";
    provider.textContent = item.provider + " / ";
    var product = document.createElement("span");
    product.textContent = item.product;
    name.appendChild(provider);
    name.appendChild(product);

    var track = document.createElement("span");
    track.className = "track";
    var servedBar = document.createElement("span");
    servedBar.className = "served";
    var declinedBar = document.createElement("span");
    declinedBar.className = "declined";
    var untriagedBar = document.createElement("span");
    untriagedBar.className = "untriaged";
    track.appendChild(servedBar);
    track.appendChild(declinedBar);
    track.appendChild(untriagedBar);

    var count = document.createElement("span");
    count.className = "count";
    var servedN = document.createElement("b");
    var rest = document.createElement("span");
    var untriagedN = document.createElement("span");
    untriagedN.className = "untriaged-n";
    count.appendChild(servedN);
    count.appendChild(rest);
    count.appendChild(untriagedN);

    el.appendChild(name);
    el.appendChild(track);
    el.appendChild(count);
    upstreamRows.appendChild(el);

    row = {
      served: servedBar, declined: declinedBar, untriaged: untriagedBar,
      servedN: servedN, rest: rest, untriagedN: untriagedN
    };
    upstreamNodes[key] = row;
    return row;
  }

  function renderUpstream(view) {
    var empty = byId("upstream-empty");
    var products = view.products || [];

    if (!view.available || products.length === 0) {
      empty.hidden = false;
      setText(empty,
        "No coverage artefact was found" + (view.source ? " in " + view.source : "") +
        ". The gap with the upstream API is unknown in this process, which is not the same " +
        "as being zero.");
    } else {
      empty.hidden = true;
    }

    products.forEach(function (item) {
      var row = upstreamRow(item.provider + "/" + item.product, item);
      var total = item.total > 0 ? item.total : 1;
      setWidth(row.served, (item.served * 100) / total);
      setWidth(row.declined, (item.declined * 100) / total);
      setWidth(row.untriaged, (item.untriaged * 100) / total);
      setText(row.servedN, item.served);
      setText(row.rest, " served of " + item.total + ", " + item.declined + " declined");
      setText(row.untriagedN, item.untriaged > 0 ? ", " + item.untriaged + " untriaged" : "");
    });

    setText(byId("upstream-source"), view.source || "—");
    setText(byId("upstream-refresh"), view.refresh || "");

    /* The date is shown as what it is. It is the artefact's file timestamp and
       not a scan date stored inside it, because the weekly workflow decides that
       the surface moved by diffing this directory: a timestamp in the file would
       open a pull request every Monday whether or not anything upstream had
       changed. Saying "written" rather than "scanned" is the whole difference
       between a provenance and a claim. */
    var written = byId("upstream-written");
    if (view.written_at) {
      var when = new Date(view.written_at);
      setText(written, "written " + when.toLocaleString([], { dateStyle: "medium", timeStyle: "short" }));
      written.title = "the timestamp of the artefact file, not of the upstream scan";
    } else {
      setText(written, "");
    }
  }

  /* ---- the refresh loop -------------------------------------------------- */

  function refresh() {
    return Promise.all([getJSON("/_feint/health"), getJSON("/_feint/conformance")])
      .then(function (answers) {
        renderHealth(answers[0]);
        renderConformance(answers[1]);
        setReachable(true);
      })
      .catch(function () {
        setReachable(false);
      });
  }

  /* The versioned artefacts are re-read on a slow beat of their own. They change
     when somebody runs the refresh command, which is weekly at best, and reading
     three files off the disk every two seconds to watch them not move would be a
     poll nobody asked for. */
  function loadData() {
    return getJSON("/_feint/ui/data").then(function (data) {
      setText(byId("version"), data.version);
      byId("version").title = data.version;
      renderUpstream(data.upstream || {});
    }).catch(function () {
      renderUpstream({ available: false });
    });
  }

  function start() {
    initTheme();
    setText(byId("endpoint"), window.location.host);

    loadData();
    window.setInterval(loadData, DATA_REFRESH_MS);

    refresh();
    window.setInterval(refresh, REFRESH_MS);
  }

  start();
}());
