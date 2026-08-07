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
    renderMachines(data);
  }

  /* ---- operation lists, the shape every drill-down uses ------------------ */

  /* One row: a status chip, the operation's upstream name, where it is mounted,
     and whatever explains it — a call count, a refusal's reason, the fields a
     handler never read. Every one of those strings is data; none is a word this
     file knows. */
  function operationRow(item, sameReasonAsAbove) {
    var li = document.createElement("li");
    li.className = sameReasonAsAbove ? "op op-continued" : "op";

    var chip = document.createElement("span");
    chip.className = "op-status";
    chip.setAttribute("data-status", item.status || "");
    chip.textContent = item.status || "";

    var name = document.createElement("span");
    name.className = "op-name mono";
    name.textContent = item.operation;

    li.appendChild(chip);
    li.appendChild(name);

    if (item.route) {
      var route = document.createElement("span");
      route.className = "op-route mono";
      route.textContent = item.route;
      li.appendChild(route);
    }
    if (item.note) {
      var note = document.createElement("span");
      note.className = "op-note";
      note.textContent = item.note;
      li.appendChild(note);
    }
    if (item.reason && !sameReasonAsAbove) {
      var reason = document.createElement("span");
      reason.className = "op-reason";
      reason.textContent = item.reason;
      li.appendChild(reason);
    }
    return li;
  }

  /* Rebuilt from scratch, and that is deliberate: these lists only exist while a
     reader is looking at one, they are hundreds of rows at most, and diffing
     them would cost more code than it saves. The refresh loop leaves them alone
     unless their source actually changed. */
  function fillOperationList(list, items, emptyMessage) {
    while (list.firstChild) { list.removeChild(list.firstChild); }
    if (items.length === 0) {
      var empty = document.createElement("li");
      empty.className = "op-empty";
      empty.textContent = emptyMessage;
      list.appendChild(empty);
      return;
    }
    /* A reason repeated on forty consecutive rows is forty times the height and
       no extra information: one product declines its whole API for a single
       argument. So a reason is printed when it changes, and the rows under it
       inherit it visually — which is also how the pack wrote it, as one Because
       over a list of operations. */
    var previous = null;
    items.forEach(function (item) {
      var repeated = item.reason !== undefined && item.reason === previous;
      previous = item.reason;
      list.appendChild(operationRow(item, repeated));
    });
  }

  function operationMatches(item, needle) {
    if (!needle) { return true; }
    return [item.operation, item.route, item.note, item.reason, item.status]
      .join(" ").toLowerCase().indexOf(needle) >= 0;
  }

  /* ---- served, driven, probed ------------------------------------------- */

  /* The route table, kept from the last refresh so a drill-down can say where an
     operation is mounted. It is the only join this page makes, and it joins on
     the operation name — the same key the coverage report joins on. */
  var routeByOperation = Object.create(null);
  var lastConformance = null;
  var openGroup = null;

  function conformanceGroups(data) {
    var calls = data.calls || {};
    var probes = data.probes || {};
    var untouched = data.untouched || [];
    var driven = [];
    Object.keys(calls).forEach(function (op) {
      if (calls[op] > 0) {
        driven.push({
          operation: op, status: "driven", route: routeByOperation[op],
          note: calls[op] + " " + plural(calls[op], "call", "calls")
        });
      }
    });
    driven.sort(function (a, b) { return a.operation < b.operation ? -1 : 1; });

    var probed = [];
    var unproven = [];
    untouched.forEach(function (op) {
      var row = { operation: op, route: routeByOperation[op] };
      if (probes[op] > 0) {
        row.status = "probed";
        row.note = probes[op] + " synthetic " + plural(probes[op], "request", "requests") +
          ", so the protocol holds and the behaviour is unproven";
        probed.push(row);
      } else {
        row.status = "unproven";
        unproven.push(row);
      }
    });

    return { driven: driven, probed: probed, unproven: unproven };
  }

  var GROUP_TITLES = {
    driven: "operations a real client has driven",
    probed: "operations only a probe has reached — schema-valid, behaviour unproven",
    unproven: "operations nobody has driven"
  };

  function renderProofDrill() {
    var drill = byId("proof-drill");
    if (!openGroup || !lastConformance) {
      drill.hidden = true;
      return;
    }
    var groups = conformanceGroups(lastConformance);
    var items = groups[openGroup] || [];
    var needle = byId("proof-drill-filter").value.trim().toLowerCase();
    var shown = items.filter(function (item) { return operationMatches(item, needle); });

    drill.hidden = false;
    setText(byId("proof-drill-title"), GROUP_TITLES[openGroup] + " (" + shown.length + ")");
    fillOperationList(byId("proof-drill-list"), shown,
      needle ? "nothing here matches that filter" : "none");
  }

  function toggleGroup(group) {
    openGroup = openGroup === group ? null : group;
    ["driven", "probed", "unproven"].forEach(function (name) {
      byId("open-" + name).setAttribute("aria-expanded", String(openGroup === name));
    });
    renderProofDrill();
  }

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
    /* Both of these were counts and nothing else, which made them the two most
       frustrating numbers on the page: they name a defect and then refuse to say
       which. They open now. */
    var violations = data.violations || {};
    var violationOps = Object.keys(violations).sort();
    var note = byId("violations-note");
    note.hidden = violationOps.length === 0;
    if (violationOps.length > 0) {
      setText(byId("violations-summary"), violationOps.length + " " +
        plural(violationOps.length, "operation", "operations") +
        " answered something its API description does not define");
      fillOperationList(byId("violations-list"), violationOps.map(function (op) {
        return {
          operation: op, status: "violation", route: routeByOperation[op],
          reason: (violations[op] || []).join(" · ")
        };
      }), "none");
    }

    var unread = data.unread_request_fields || {};
    var unreadOps = Object.keys(unread).sort();
    var unreadNote = byId("unread-note");
    unreadNote.hidden = unreadOps.length === 0;
    if (unreadOps.length > 0) {
      setText(byId("unread-summary"), unreadOps.length + " " +
        plural(unreadOps.length, "operation", "operations") + " received a field no handler read");
      fillOperationList(byId("unread-list"), unreadOps.map(function (op) {
        return {
          operation: op, status: "unread", route: routeByOperation[op],
          reason: "sent, never read: " + (unread[op] || []).join(" · ")
        };
      }), "none");
    }

    lastConformance = data;
    renderProofDrill();
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

    /* The row is a disclosure, so the counts open onto the operations they are
       made of. 111 declined is not something a reader can act on; the sentence
       written beside each refusal is, and it has been in the pack all along. */
    var el = document.createElement("details");
    el.className = "row-details";

    var summary = document.createElement("summary");
    summary.className = "row";

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

    summary.appendChild(name);
    summary.appendChild(track);
    summary.appendChild(count);
    el.appendChild(summary);

    var body = document.createElement("div");
    body.className = "row-body";
    var filter = document.createElement("input");
    filter.type = "search";
    filter.className = "search";
    filter.placeholder = "filter these operations";
    filter.autocomplete = "off";
    filter.spellcheck = false;
    var list = document.createElement("ol");
    list.className = "op-list";
    body.appendChild(filter);
    body.appendChild(list);
    el.appendChild(body);
    upstreamRows.appendChild(el);

    row = {
      served: servedBar, declined: declinedBar, untriaged: untriagedBar,
      servedN: servedN, rest: rest, untriagedN: untriagedN,
      list: list, filter: filter, operations: [], rendered: ""
    };

    var draw = function () {
      var needle = filter.value.trim().toLowerCase();
      var shown = row.operations.filter(function (op) { return operationMatches(op, needle); });
      fillOperationList(list, shown, needle ? "nothing here matches that filter" : "none");
    };
    filter.addEventListener("input", draw);
    /* Filled on first open rather than on every refresh: 923 operations across
       eight products is a lot of nodes to build for panels nobody has opened. */
    el.addEventListener("toggle", function () {
      if (el.open) { draw(); }
    });
    row.draw = draw;

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

    /* Grouped by the product each operation belongs to, using the provider and
       product the artefact recorded. No name is matched against anything this
       file knows. */
    var byProduct = Object.create(null);
    (view.operations || []).forEach(function (op) {
      var key = op.provider + "/" + op.product;
      if (!byProduct[key]) { byProduct[key] = []; }
      byProduct[key].push(op);
    });

    products.forEach(function (item) {
      var key = item.provider + "/" + item.product;
      var row = upstreamRow(key, item);
      var total = item.total > 0 ? item.total : 1;
      setWidth(row.served, (item.served * 100) / total);
      setWidth(row.declined, (item.declined * 100) / total);
      setWidth(row.untriaged, (item.untriaged * 100) / total);
      setText(row.servedN, item.served);
      setText(row.rest, " served of " + item.total + ", " + item.declined + " declined");
      setText(row.untriagedN, item.untriaged > 0 ? ", " + item.untriaged + " untriaged" : "");

      /* Served first, then untriaged, then declined: the first two are work and
         the third is a decision already taken, and a reader scanning for
         something to do should meet the work first. */
      var rank = { implemented: 0, unknown: 1, declined: 2 };
      var operations = (byProduct[key] || []).slice().sort(function (a, b) {
        if (rank[a.status] !== rank[b.status]) { return rank[a.status] - rank[b.status]; }
        return a.operation < b.operation ? -1 : 1;
      });
      var fingerprint = JSON.stringify(operations);
      if (row.rendered !== fingerprint) {
        row.rendered = fingerprint;
        row.operations = operations.map(function (op) {
          return {
            operation: op.operation,
            status: op.status === "implemented" ? "served" : op.status === "unknown" ? "untriaged" : op.status,
            route: routeByOperation[op.operation],
            reason: op.reason,
            note: op.version
          };
        });
        if (row.list.parentNode && row.list.parentNode.parentNode.open) { row.draw(); }
      }
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

  /* ---- machines ---------------------------------------------------------- */

  /* Built only when a runtime is configured, and from what that runtime
     declares. Never deduced from the mode name: that is the rule docs/limits.md
     states, and the reason it exists is that somebody running one mode and
     reading a README about another finds out when a test that should fail
     passes.

     With no runtime the region is absent from the document rather than hidden by
     a rule. A region styled away is a region a reader finds in the inspector and
     believes. */

  var machinesSlot = byId("machines-slot");
  var machinesCard = null;

  /* The order is the one docs/limits.md argues in: machines, then what they
     carry, then the one capability that separates the modes. The labels are this
     page's words for the fields the health payload publishes; the verdicts are
     never this page's. */
  var CAPABILITIES = [
    { key: "machines", label: "machines" },
    { key: "addresses", label: "addresses" },
    { key: "firewall", label: "firewall" },
    { key: "isolation", label: "isolation" },
    { key: "own_kernel", label: "own kernel" }
  ];

  function buildMachinesCard() {
    var card = document.createElement("section");
    card.className = "card";
    card.id = "machines-card";

    var head = document.createElement("div");
    head.className = "card-head";
    var title = document.createElement("h2");
    title.textContent = "machines";
    var hint = document.createElement("span");
    hint.className = "hint";
    hint.textContent = "declared by the runtime, never deduced";
    head.appendChild(title);
    head.appendChild(hint);
    card.appendChild(head);

    var driverLine = document.createElement("p");
    driverLine.className = "driver-line";
    var driverLabel = document.createElement("span");
    driverLabel.className = "key";
    driverLabel.textContent = "driver";
    var driverName = document.createElement("span");
    driverName.className = "name";
    driverLine.appendChild(driverLabel);
    driverLine.appendChild(driverName);
    card.appendChild(driverLine);

    var caps = document.createElement("ul");
    caps.className = "caps";
    var chips = {};
    CAPABILITIES.forEach(function (cap) {
      var li = document.createElement("li");
      li.className = "cap";
      var glyph = document.createElement("span");
      glyph.className = "glyph";
      var label = document.createElement("span");
      label.textContent = cap.label;
      li.appendChild(glyph);
      li.appendChild(label);
      caps.appendChild(li);
      chips[cap.key] = { chip: li, glyph: glyph };
    });
    card.appendChild(caps);

    var note = document.createElement("p");
    note.className = "note";
    card.appendChild(note);

    machinesSlot.appendChild(card);
    document.querySelector("main").classList.add("has-machines");
    return { card: card, driver: driverName, chips: chips, note: note };
  }

  function renderMachines(data) {
    var driver = data.machines || "none";
    if (driver === "none") {
      /* Removed, not hidden: with no runtime there is nothing to say, and a
         region that says nothing is a region that invites a reader to wonder
         what it would have said. */
      if (machinesCard) {
        machinesSlot.removeChild(machinesCard.card);
        document.querySelector("main").classList.remove("has-machines");
        machinesCard = null;
      }
      return;
    }
    if (!machinesCard) { machinesCard = buildMachinesCard(); }
    setText(machinesCard.driver, driver);

    /* null means the driver declared nothing at all, which is not the same as
       declaring everything false — and the page must never turn silence into a
       refusal on a driver's behalf. */
    var declared = data.capabilities;
    CAPABILITIES.forEach(function (cap) {
      var node = machinesCard.chips[cap.key];
      if (!declared) {
        node.chip.setAttribute("data-state", "undeclared");
        setText(node.glyph, "?");
        node.chip.title = "this driver declares no capabilities";
        return;
      }
      var yes = declared[cap.key] === true;
      node.chip.setAttribute("data-state", yes ? "yes" : "no");
      setText(node.glyph, yes ? "✓" : "✗");
      node.chip.title = yes
        ? "declared by this runtime"
        : "this runtime declares it does not deliver this";
    });

    setText(machinesCard.note, declared
      ? "What the runtime reports about these machines arrives in the call log, on the same timeline as the calls."
      : "This driver declares no capabilities, so nothing here is claimed about it — which is different from claiming it delivers none.");
  }

  /* ---- the call log ------------------------------------------------------ */

  /* The stream is one-way by construction — text/event-stream carries nothing
     back — so opening it can never become a way to drive the emulator. The
     browser reconnects on its own, and the ring means a reconnection loses
     nothing that is still in it. */

  var LOG_MAX = 256;
  var logList = byId("log");
  var liveBadge = byId("live");
  var paused = false;
  var problemsOnly = false;
  var pendingWhilePaused = [];
  var lastSeq = 0;
  var source = null;

  function timeOf(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return ""; }
    var pad = function (n, width) {
      var s = String(n);
      while (s.length < width) { s = "0" + s; }
      return s;
    };
    return pad(d.getHours(), 2) + ":" + pad(d.getMinutes(), 2) + ":" +
      pad(d.getSeconds(), 2) + "." + pad(d.getMilliseconds(), 3);
  }

  function durationOf(ms) {
    if (ms >= 1000) { return (ms / 1000).toFixed(2) + " s"; }
    if (ms >= 10) { return ms.toFixed(1) + " ms"; }
    return ms.toFixed(2) + " ms";
  }

  /* A status class rather than a colour: 2xx is not "good" here, it is
     "answered", and a 4xx from a refusal the emulator meant is not a failure. */
  function statusClass(status, mounted) {
    if (!mounted) { return "missing"; }
    if (status >= 500) { return "server"; }
    if (status >= 400) { return "client"; }
    return "ok";
  }

  function hasProblem(x) {
    return !x.mounted || x.status >= 400 ||
      (x.unread && x.unread.length > 0) || (x.violations && x.violations.length > 0);
  }

  function callEntry(x) {
    var li = document.createElement("li");
    li.className = "entry";
    li.setAttribute("data-class", statusClass(x.status, x.mounted));
    if (!hasProblem(x)) { li.setAttribute("data-plain", "yes"); }

    var head = document.createElement("div");
    head.className = "head";
    var when = document.createElement("time");
    when.textContent = timeOf(x.t);
    when.dateTime = x.t;
    var method = document.createElement("span");
    method.className = "method";
    method.setAttribute("data-verb", x.method);
    method.textContent = x.method;
    var path = document.createElement("span");
    path.className = "path";
    path.textContent = x.query ? x.path + "?" + x.query : x.path;
    head.appendChild(when);
    head.appendChild(method);
    head.appendChild(path);
    li.appendChild(head);

    var meta = document.createElement("div");
    meta.className = "meta";
    var code = document.createElement("span");
    code.className = "code";
    code.textContent = x.status;
    var dur = document.createElement("span");
    dur.className = "dur";
    dur.textContent = durationOf(x.ms || 0);
    meta.appendChild(code);
    meta.appendChild(dur);

    var what = document.createElement("span");
    what.className = "op";
    /* An operation nobody mounted is the line worth reading: it is how a plan
       dies, and no counter anywhere else records which route it died on. */
    what.textContent = x.operation || "no route mounted";
    meta.appendChild(what);
    li.appendChild(meta);

    if (x.unread && x.unread.length > 0) {
      li.appendChild(verdict("unread", "sent, never read: ", x.unread.join(" · ")));
    }
    if (x.violations && x.violations.length > 0) {
      li.appendChild(verdict("violation", "the API description does not define: ", x.violations.join(" · ")));
    }
    return li;
  }

  function verdict(kind, label, detail) {
    var line = document.createElement("div");
    line.className = "verdict " + kind;
    var name = document.createElement("span");
    name.textContent = label;
    var fields = document.createElement("span");
    fields.className = "fields";
    fields.textContent = detail;
    line.appendChild(name);
    line.appendChild(fields);
    return line;
  }

  function runtimeEntry(e) {
    var li = document.createElement("li");
    li.className = "entry runtime";
    var head = document.createElement("div");
    head.className = "head";
    var when = document.createElement("time");
    when.textContent = timeOf(e.at);
    var what = document.createElement("span");
    what.className = "action";
    what.textContent = e.action || e.kind || "runtime";
    var target = document.createElement("span");
    target.className = "target";
    target.textContent = e.resource || "";
    head.appendChild(when);
    head.appendChild(what);
    head.appendChild(target);
    li.appendChild(head);
    if (e.message) {
      var meta = document.createElement("div");
      meta.className = "meta";
      var text = document.createElement("span");
      text.textContent = e.message;
      meta.appendChild(text);
      li.appendChild(meta);
    }
    return li;
  }

  /* A gap is shown, never smoothed over. The ring drops the oldest entry when it
     is full and a slow reader is skipped rather than waited for, so a jump in
     the sequence is a fact the page has to state instead of a silence it can
     let pass for completeness. */
  function gapEntry(missed) {
    var li = document.createElement("li");
    li.className = "entry gap";
    li.textContent = missed + " " + plural(missed, "call", "calls") + " not shown";
    return li;
  }

  function append(node) {
    /* Stuck to the bottom only when the reader already was: scrolling up to
       read a line and being yanked back down by the next call is how a live log
       becomes unusable. */
    var atBottom = logList.scrollHeight - logList.scrollTop - logList.clientHeight < 24;
    logList.appendChild(node);
    while (logList.childElementCount > LOG_MAX) {
      logList.removeChild(logList.firstChild);
    }
    byId("log-empty").hidden = true;
    if (atBottom) { logList.scrollTop = logList.scrollHeight; }
  }

  function show(entry) {
    if (entry.seq && lastSeq && entry.seq > lastSeq + 1) {
      append(gapEntry(entry.seq - lastSeq - 1));
    }
    if (entry.seq) { lastSeq = entry.seq; }
    if (entry.kind !== undefined || entry.action !== undefined) {
      append(runtimeEntry(entry));
      return;
    }
    append(callEntry(entry));
  }

  function record(entry) {
    if (paused) {
      pendingWhilePaused.push(entry);
      if (pendingWhilePaused.length > LOG_MAX) { pendingWhilePaused.shift(); }
      setText(byId("buffered"), pendingWhilePaused.length + " " +
        plural(pendingWhilePaused.length, "call", "calls") + " while paused");
      byId("buffered").hidden = false;
      return;
    }
    show(entry);
  }

  function applyFilter() {
    var entries = logList.children;
    for (var i = 0; i < entries.length; i++) {
      var plain = entries[i].getAttribute("data-plain") === "yes";
      entries[i].classList.toggle("filtered", problemsOnly && plain);
    }
  }

  function openStream() {
    if (source) { source.close(); }
    source = new EventSource("/_feint/events");
    source.addEventListener("call", function (event) {
      record(JSON.parse(event.data));
      applyFilter();
    });
    source.addEventListener("runtime", function (event) {
      record(JSON.parse(event.data));
      applyFilter();
    });
    source.addEventListener("open", function () {
      liveBadge.setAttribute("data-state", paused ? "paused" : "on");
    });
    source.addEventListener("error", function () {
      /* EventSource reconnects on its own, so this is a state to show and not a
         failure to handle. The ring means the reconnection replays what was
         missed, as far back as it still holds. */
      liveBadge.setAttribute("data-state", "off");
    });
  }

  function initLog() {
    byId("pause").addEventListener("click", function () {
      paused = !paused;
      this.setAttribute("aria-pressed", String(paused));
      setText(this, paused ? "resume" : "pause");
      liveBadge.setAttribute("data-state", paused ? "paused" : "on");
      if (!paused) {
        pendingWhilePaused.forEach(show);
        pendingWhilePaused = [];
        byId("buffered").hidden = true;
        applyFilter();
      }
    });
    byId("filter-problems").addEventListener("click", function () {
      problemsOnly = !problemsOnly;
      this.setAttribute("aria-pressed", String(problemsOnly));
      applyFilter();
    });
    byId("clear").addEventListener("click", function () {
      while (logList.firstChild) { logList.removeChild(logList.firstChild); }
      byId("log-empty").hidden = false;
    });
    openStream();
  }

  /* ---- the refresh loop -------------------------------------------------- */

  function refresh() {
    return Promise.all([
      getJSON("/_feint/health"),
      getJSON("/_feint/conformance"),
      getJSON("/_feint/resources"),
      getJSON("/_feint/routes")
    ])
      .then(function (answers) {
        /* The route table first: every drill-down below wants to say where an
           operation is mounted, and an empty map would silently drop that
           column on the first render. */
        answers[3].forEach(function (route) {
          routeByOperation[route.operation] = route.method + " " + route.path;
        });
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

    ["driven", "probed", "unproven"].forEach(function (group) {
      byId("open-" + group).addEventListener("click", function () { toggleGroup(group); });
    });
    byId("proof-drill-filter").addEventListener("input", renderProofDrill);
    byId("proof-drill-close").addEventListener("click", function () {
      if (openGroup) { toggleGroup(openGroup); }
    });

    initLog();

    loadData();
    window.setInterval(loadData, DATA_REFRESH_MS);

    refresh();
    window.setInterval(refresh, REFRESH_MS);
  }

  start();
}());
