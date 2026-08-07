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

  /* ---- rendering values nobody wrote a schema for ------------------------ */

  var MAX_INLINE = 120;
  var MAX_DEPTH = 8;

  /* A resource's attributes are the provider's own body, and this page knows
     none of its keys. Rendering a chosen subset would mean maintaining a list of
     interesting fields, and the day it goes stale is the day it hides the field
     somebody was looking for — so the whole map is rendered, structure included,
     and folded so that whole is not the same as unreadable.

     Disclosure is <details>, not a click handler: the browser already knows how
     to open, close, focus and announce one, and an element that keeps its own
     state survives a refresh without any bookkeeping here. */
  function valueNode(value, depth) {
    if (value === null || value === undefined) {
      return leaf("null", "null");
    }
    var type = typeof value;
    if (type === "boolean" || type === "number") {
      return leaf(String(value), type);
    }
    if (type === "string") {
      return stringNode(value);
    }
    if (depth >= MAX_DEPTH) {
      return leaf("…", "null");
    }
    if (Array.isArray(value)) {
      if (value.length === 0) { return leaf("[]", "null"); }
      return branch("[" + value.length + "]", value.map(function (item, i) {
        return entryNode(String(i), item, depth + 1);
      }));
    }
    var keys = Object.keys(value);
    if (keys.length === 0) { return leaf("{}", "null"); }
    return branch("{" + keys.length + "}", keys.map(function (key) {
      return entryNode(key, value[key], depth + 1);
    }));
  }

  function leaf(text, kind) {
    var el = document.createElement("span");
    el.className = "val-" + kind;
    el.textContent = text;
    return el;
  }

  /* Long strings are cut and revealed on click rather than wrapped: a base64
     cloud-init payload is one value among thirty, and letting it push the other
     twenty-nine off the screen makes the panel useless exactly when it is
     interesting. */
  function stringNode(text) {
    if (text.length <= MAX_INLINE) { return leaf(text, "string"); }
    var el = document.createElement("button");
    el.type = "button";
    el.className = "val-string clipped";
    el.title = "click to show the whole value";
    el.textContent = text.slice(0, MAX_INLINE) + "…";
    el.addEventListener("click", function () {
      var open = el.classList.toggle("open");
      el.textContent = open ? text : text.slice(0, MAX_INLINE) + "…";
    });
    return el;
  }

  function branch(label, children) {
    var el = document.createElement("details");
    el.className = "tree";
    var head = document.createElement("summary");
    head.textContent = label;
    el.appendChild(head);
    children.forEach(function (child) { el.appendChild(child); });
    return el;
  }

  function entryNode(key, value, depth) {
    var row = document.createElement("div");
    row.className = "tree-row";
    var name = document.createElement("span");
    name.className = "tree-key";
    name.textContent = key;
    row.appendChild(name);
    row.appendChild(valueNode(value, depth));
    return row;
  }

  /* ---- time ------------------------------------------------------------- */

  /* Relative on the line, absolute in the tooltip. "4 min ago" answers the
     question being asked — did my command just do that — and the timestamp is
     there for the one time it is not. */
  function relative(iso) {
    var then = new Date(iso).getTime();
    if (isNaN(then)) { return ""; }
    var seconds = Math.round((Date.now() - then) / 1000);
    if (seconds < 5) { return "just now"; }
    if (seconds < 90) { return seconds + "s ago"; }
    var minutes = Math.round(seconds / 60);
    if (minutes < 90) { return minutes + " min ago"; }
    var hours = Math.round(minutes / 60);
    if (hours < 36) { return hours + " h ago"; }
    return Math.round(hours / 24) + " d ago";
  }

  /* ---- the inventory ----------------------------------------------------- */

  var inventoryGroups = byId("inventory-groups");
  var inventorySearch = byId("inventory-search");
  var groupNodes = Object.create(null);
  var resourceNodes = Object.create(null);
  var lastResources = [];

  function groupFor(provider, kind) {
    var key = provider + " " + kind;
    var group = groupNodes[key];
    if (group) { return group; }

    var el = document.createElement("details");
    el.className = "group";
    el.open = true;
    var head = document.createElement("summary");

    var providerName = document.createElement("span");
    providerName.className = "group-provider";
    providerName.textContent = provider || "unattributed";
    var kindName = document.createElement("span");
    kindName.className = "group-kind";
    kindName.textContent = kind;
    var count = document.createElement("span");
    count.className = "group-count";

    head.appendChild(providerName);
    head.appendChild(kindName);
    head.appendChild(count);
    el.appendChild(head);

    var body = document.createElement("div");
    body.className = "group-body";
    el.appendChild(body);
    inventoryGroups.appendChild(el);

    group = { el: el, body: body, count: count };
    groupNodes[key] = group;
    return group;
  }

  function resourceCard(item) {
    var key = item.provider + " " + item.kind + " " + item.id;
    var card = resourceNodes[key];
    if (card) { return card; }

    var el = document.createElement("details");
    el.className = "resource";

    var head = document.createElement("summary");
    var state = document.createElement("span");
    state.className = "state";
    var id = document.createElement("span");
    id.className = "res-id mono";
    id.textContent = item.id;
    var name = document.createElement("span");
    name.className = "res-name";
    var age = document.createElement("span");
    age.className = "res-age";

    head.appendChild(state);
    head.appendChild(id);
    head.appendChild(name);
    head.appendChild(age);
    el.appendChild(head);

    var body = document.createElement("div");
    body.className = "resource-body";

    var actions = document.createElement("div");
    actions.className = "resource-actions";
    var copy = document.createElement("button");
    copy.type = "button";
    copy.className = "ghost";
    copy.textContent = "copy id";
    copy.addEventListener("click", function () {
      copyText(item.id, copy);
    });
    actions.appendChild(copy);

    var dates = document.createElement("span");
    dates.className = "res-dates";
    actions.appendChild(dates);

    body.appendChild(actions);

    var attrs = document.createElement("div");
    attrs.className = "attrs";
    body.appendChild(attrs);

    el.appendChild(body);

    card = { el: el, state: state, name: name, age: age, dates: dates, attrs: attrs, rendered: "" };
    resourceNodes[key] = card;
    return card;
  }

  function copyText(text, button) {
    var restore = function () { setText(button, "copy id"); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () {
        setText(button, "copied");
        window.setTimeout(restore, 1200);
      }, function () {
        setText(button, "copy failed");
        window.setTimeout(restore, 1200);
      });
      return;
    }
    setText(button, "copy failed");
    window.setTimeout(restore, 1200);
  }

  /* The filter is one string against everything the page knows about a
     resource: an id, a type, a state, a provider, a zone. Past the tenth
     resource it is what makes the region usable, and a single box beats four
     dropdowns nobody configures. */
  function matchesFilter(item, needle) {
    if (!needle) { return true; }
    var hay = [item.id, item.kind, item.provider, item.state, item.zone, item.project]
      .join(" ").toLowerCase();
    if (hay.indexOf(needle) >= 0) { return true; }
    // The attributes too, so searching for an address or a name a client chose
    // finds the resource carrying it.
    try {
      return JSON.stringify(item.attrs || {}).toLowerCase().indexOf(needle) >= 0;
    } catch (e) {
      return false;
    }
  }

  function renderInventory(data) {
    lastResources = data.resources || [];
    var needle = inventorySearch.value.trim().toLowerCase();
    var shown = 0;
    var seen = Object.create(null);
    var perGroup = Object.create(null);

    lastResources.forEach(function (item) {
      if (!matchesFilter(item, needle)) { return; }
      shown++;

      var groupKey = item.provider + " " + item.kind;
      perGroup[groupKey] = (perGroup[groupKey] || 0) + 1;
      var group = groupFor(item.provider, item.kind);

      var key = groupKey + " " + item.id;
      seen[key] = true;
      var card = resourceCard(item);
      if (card.el.parentNode !== group.body) { group.body.appendChild(card.el); }

      setText(card.state, item.state || "—");
      card.state.setAttribute("data-state", (item.state || "").toLowerCase());
      setText(card.name, readableName(item));
      setText(card.age, relative(item.updated || item.created));
      card.age.title = "created " + item.created + "\nupdated " + item.updated;
      setText(card.dates, "created " + relative(item.created) + ", updated " + relative(item.updated));

      /* The attribute tree is rebuilt only when the attributes changed. It is
         the one part of this page that can be hundreds of nodes, and rebuilding
         it every two seconds would close every branch the reader had opened. */
      var fingerprint = JSON.stringify([item.state, item.updated, item.attrs, item.runtime]);
      if (card.rendered !== fingerprint) {
        card.rendered = fingerprint;
        while (card.attrs.firstChild) { card.attrs.removeChild(card.attrs.firstChild); }
        Object.keys(item.attrs || {}).sort().forEach(function (name) {
          card.attrs.appendChild(entryNode(name, item.attrs[name], 0));
        });
        if (item.runtime) {
          var runtime = document.createElement("div");
          runtime.className = "runtime-block";
          var label = document.createElement("div");
          label.className = "runtime-label";
          label.textContent = "runtime — what backs this here, never sent to a client";
          runtime.appendChild(label);
          Object.keys(item.runtime).sort().forEach(function (name) {
            runtime.appendChild(entryNode(name, item.runtime[name], 0));
          });
          card.attrs.appendChild(runtime);
        }
      }
    });

    // Anything that went away goes away here too: a resource a client deleted
    // must not linger on a page claiming to show what exists.
    Object.keys(resourceNodes).forEach(function (key) {
      if (seen[key]) { return; }
      var card = resourceNodes[key];
      if (card.el.parentNode) { card.el.parentNode.removeChild(card.el); }
      delete resourceNodes[key];
    });
    Object.keys(groupNodes).forEach(function (key) {
      var group = groupNodes[key];
      var n = perGroup[key] || 0;
      setText(group.count, n);
      group.el.hidden = n === 0;
    });

    setText(byId("inventory-count"),
      needle ? shown + " of " + lastResources.length + " shown" : lastResources.length + " " + plural(lastResources.length, "resource", "resources"));
    byId("inventory-empty").hidden = lastResources.length > 0;
  }

  /* A name if the pack recorded one under a key it chose, and nothing if it did
     not. The keys are tried in order and none of them is a provider's: they are
     the words APIs use, and a pack that uses another simply shows no name. */
  function readableName(item) {
    var attrs = item.attrs || {};
    var candidates = ["name", "Name", "hostname", "display_name", "label"];
    for (var i = 0; i < candidates.length; i++) {
      var value = attrs[candidates[i]];
      if (typeof value === "string" && value !== "") { return value; }
    }
    return "";
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
    return Promise.all([
      getJSON("/_feint/health"),
      getJSON("/_feint/conformance"),
      getJSON("/_feint/resources")
    ])
      .then(function (answers) {
        renderHealth(answers[0]);
        renderConformance(answers[1]);
        renderInventory(answers[2]);
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

    /* Filtering redraws from the answer already in hand rather than asking the
       emulator again: typing must not put a request on the wire per keystroke,
       and the data is two seconds old at worst. */
    inventorySearch.addEventListener("input", function () {
      renderInventory({ resources: lastResources });
    });

    loadData();
    window.setInterval(loadData, DATA_REFRESH_MS);

    refresh();
    window.setInterval(refresh, REFRESH_MS);
  }

  start();
}());
