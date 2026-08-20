# Reprise du lot #341 + #342 (jalon 0.10.0)

> **Note d'intégration, ajoutée par le coordinateur.** L'agent qui a écrit ce
> document a épuisé son crédit *avant* de committer : il écrivait « commité » au
> futur. Le commit a été fait à sa place, tel quel, sans rien modifier de son
> travail — l'arbre compile et `go test -race ./internal/core/machine/
> ./internal/cli/` passe, vérifié avant le commit. Ce qui suit décrit donc
> exactement le contenu de ce commit, mais **`mise run prepush` n'a pas été
> rejoué** et les deux passages OVN consécutifs exigés par #341 n'ont pas eu
> lieu.

Base : `1df41a1` (main au moment du départ). **`main` a avancé depuis avec #337
(`d735385`, NIC routée, capacité `firewall_public_only`, schéma santé v5) : il
faudra rebaser.** Les sujets ne se recouvrent pas (doctor et uplink ici, NIC
là-bas), le conflit devrait être nul ou trivial.

## Ce qui est FAIT et prouvé

Tout ce qui suit est commité, compile, et passe `go test -race
./internal/core/machine/ ./internal/cli/`. Les trois specs de falsification
passent (`compiled=yes / the test bit` sur les 11 mutations) :

```
python3 tools/falsify/falsify.py tools/falsify/specs/dhcp-leftover-ownership.json
python3 tools/falsify/falsify.py tools/falsify/specs/uplink-serialisation.json
python3 tools/falsify/falsify.py tools/falsify/specs/dhcp-doctor-network-question.json
```

## #341 — LA CAUSE, NOMMÉE ET MESURÉE (ne pas re-payer cette mesure)

Un passage complet `FEINT_VM=incus-ovn mise run conformance` sur cette station
(2026-08-20, 297 s, exit 1) a reproduit l'échec exact de l'issue **du premier
coup, depuis une station propre** — la théorie « état accumulé de runs
précédents » est donc partiellement démentie, voir Prémisses. Artefacts :

- `/tmp/claude-1000/-home-bob-Projets-coucou/642e21ac-a973-45b5-ac8e-3ed5bef14b39/scratchpad/pass-baseline.log` (la suite outscale-tofu meurt sur `delegate 10.70.1.0/24 … Failed deleting nftables chain "fwd.feint-uplink" … No such file or directory`, lignes ~1668)
- `…/scratchpad/monitor-baseline.log` (`incus monitor --pretty --type=logging --loglevel=debug` pendant tout le passage — **c'est la pièce à conviction**, lignes 5115–5145)
- `…/scratchpad/watch-baseline.log` (routes de l'uplink + chaînes nftables toutes les 2 s)

La chaîne causale, lisible dans monitor-baseline.log à 20:39:34–35 :

1. `tools/conformance/outscale/oapi-cli.sh:785` lance **`feint clean` en fin de
   suite** : l'uplink est vidé (`Update … ipv4.routes:` vide, ligne 4979) puis
   **supprimé**. La suite outscale-tofu, juste derrière, doit donc tout
   recréer d'un coup.
2. tofu (parallélisme par défaut) crée en même temps : le subnet 10.70.1.0/24,
   le subnet azb 10.70.2.0/24, et une VM sans subnet qui déclenche la création
   du réseau machine par défaut `fnt-default`.
3. Côté feint, la délégation d'un bloc est un `incus network set feint-uplink
   ipv4.routes=…` (`delegateRoute`), qui ne prenait **pas** `uplinkMu` (le
   verrou ne couvrait que `setUplinkRoute`) ; la création `network create
   --type=ovn network=feint-uplink` n'était pas verrouillée non plus.
4. Côté Incus 7.2 (source lu dans
   `…/scratchpad/incus-src`) : un `PUT /1.0/networks/feint-uplink` (bridge
   `Update → setup → Firewall.NetworkClear`) **et** un `POST /1.0/networks`
   créant un réseau OVN attaché à l'uplink **reconstruisent tous deux le
   pare-feu nftables de l'uplink** (mesuré : `Clearing firewall
   driver=bridge network=feint-uplink` sous les deux requêtes, lignes 5133 et
   5143). `removeChains` (`internal/server/firewall/drivers/drivers_nftables.go:766`)
   est un **snapshot-puis-suppression** : il liste le ruleset, puis émet un
   `nft flush chain … ; delete chain …` par chaîne trouvée. Les deux chemins ne
   partagent **aucun verrou** dans le démon (`networkPut` n'en prend pas ; le
   verrou `network.ovn.<uplink>` ne couvre que les opérations de port OVN).
   Donc : la chaîne est **supprimée par l'autre requête d'abord**, et le
   perdant meurt sur ENOENT. C'est la réponse à la question de l'issue
   (« supprimée deux fois / par autre chose d'abord / jamais créée ») :
   **supprimée par l'opération concurrente entre le snapshot du perdant et sa
   suppression**.
5. Pourquoi « seule une jambe complète le produit » : en jambe isolée,
   `feint-uplink` et `fnt-default` **survivent du run précédent** (restes
   volontairement gardés) — pas de création concurrente, pas de course. Le
   passage complet intercale le `feint clean` d'oapi-cli, qui force la
   recréation simultanée.

Corroboration de la moitié « restes » : dans CE MÊME passage, l'uplink portait
déjà 9 routes accumulées (10.182–10.186, 172.16.8/9, 10.190.x) avant le clean —
`RemoveNetwork` ne retirait jamais le bloc délégué. Les « sept routes » de
l'issue s'accumulent **dans un seul passage**, pas seulement entre passages.

### Le correctif (commité)

`internal/core/machine/incus.go` :
- `uplinkMu` re-documenté : il sérialise désormais **toute** opération qui fait
  reconstruire l'uplink par le démon. `EnsureNetwork` (branche OVN) tient le
  verrou sur ensureUplink + delegateRoute + **le `network create` lui-même**.
- `RemoveNetwork` : lit le bloc du réseau avant suppression
  (`networkGateway` → `Masked()`), le **retire des routes de l'uplink après une
  suppression réussie** (`dropUplinkRouteOVN`), sous le même verrou.
- `networkCreateError` : nomme aussi un service DHCP **étranger** qui tient une
  adresse du bloc demandé (seam `holderScan`), sans jamais proposer de le tuer.

`internal/core/machine/incus_ovn.go` :
- `delegateRoute`/`ensureUplink` : appelant tient `uplinkMu` ;
  `addUplinkRoute`/`dropUplinkRoute` factorisent l'édition (écriture évitée
  quand rien ne change — chaque écriture est une reconstruction du pare-feu).
- `adoptUplink` (nouveau) : à la première réutilisation d'un uplink laissé par
  un run mort, **les routes des réseaux disparus sont retirées** (on ne garde
  que les blocs des réseaux OVN étiquetés encore debout), une fois par
  processus (`uplinkAdopt sync.Once`, brûlé à la création pour ne pas
  « réconcilier » nos propres /32).
- `UplinkHolderKey` (`user.feint.holder` = pid) : un uplink tenu par un **autre
  émulateur vivant** est **refusé** au lieu d'être partagé (le partage
  inter-processus est la même corruption sans verrou possible). Un pid mort est
  repris. Seam `holderProbe` pour les tests.

Tests : `internal/core/machine/incus_uplink_test.go` (5 tests, dont la
sérialisation prouvée par un runner qui détecte deux reconstructions en vol).
Falsification : `tools/falsify/specs/uplink-serialisation.json` (5 mutations,
toutes mordent).

## #342 — fait aussi

La question posée passe de l'interface au **réseau**. `classifyDHCP`
(`internal/core/machine/leftover.go`) : un dnsmasq aux interfaces toutes
`fnt-` est un reste si chaque interface est disparue **ou** si l'objet réseau
n'existe plus dans **aucun projet** du runtime (`incus query
/1.0/networks?recursion=1&all-projects=true`, prédicat conservateur trois fois
— runtime muet, JSON illisible, nom trouvé quelque part ⇒ pas un reste) **et**
que le processus tourne sur `/var/lib/incus/networks/<iface>/` (preuve la plus
forte exigée quand le sujet est vivant). Nouveau champ
`DHCPLeftover.InterfaceAlive`.

- `feint doctor` (`internal/cli/doctor.go`) : le reste est un **FAIL** (rouge),
  nomme bloc et pid ; la ligne verte dit « no DHCP service of the emulator's
  outlives its network » avec le périmètre mesuré en détail.
- `feint clean` (`internal/cli/clean.go`) : tue le service, et **dit ce qu'il
  ne touchera pas** — le pont survivant, sans étiquette, n'est pas
  démontrablement à nous (`sudo ip link delete <pont>` proposé à l'opérateur).
- Relecture des lignes vertes de doctor (le point 4 de l'issue) — reformulées :
  images (« le daemon ssh est la promesse de la recette, pas de ce contrôle »),
  env hazards (« la liste mesurée du pack, pas tout le shell »), ProxyJump
  (« les deux motifs mesurés, * et 10.* » ; « could not look » devient warn au
  lieu d'un ok).
- `DHCPHolders(block)` : la moitié « à nous ou non » de la question, répondue
  au seul moment où le bloc voulu est connu : l'erreur de création.

## Ce qui RESTE, dans l'ordre

1. **Rejouer `mise run prepush`** (jamais rejoué après les derniers edits ;
   `golangci-lint cache clean` d'abord si le lint parle de worktrees
   supprimés — piège rencontré deux fois le 2026-08-20).
2. **Entrées `## [Unreleased]` dans les DEUX changelogs** (CHANGELOG.md et
   CHANGELOG.fr.md) — non faites.
3. **LE CRITÈRE DE #341 : deux passages complets `FEINT_VM=incus-ovn mise run
   conformance` verts d'affilée sur cette station** (~5 min chacun ici). Non
   lancés après correctif — c'est la preuve d'acceptation, sans elle le lot
   n'est pas clos. Utiliser
   `…/scratchpad/run-pass.sh <label>` qui capture aussi monitor incus +
   watcher (état avant/après inclus).
4. Vérifier que la suite ssh/balancer OVN accepte le refus d'uplink partagé
   (aucun scénario connu ne partage, zones.sh tourne sans --vm, mais le
   passage complet tranchera).
5. Rebaser sur main (#337). Commit final au format du dépôt, PR non ouverte
   (consigne : commits locaux seulement).

## Pièges rencontrés

- **`mise` non « trusted » dans un worktree neuf** : `mise trust` d'abord,
  sinon tout `mise run` échoue.
- Les patterns de `fakeRuntime` (answers/fail) matchent par **sous-chaîne** sur
  la commande jointe : choisir des clés qui ne se recouvrent pas
  (`ipv4.address` vs `ipv4.routes`).
- Le harnais falsify **refuse toute mutation qui perd un identifiant** (même un
  const) : neutraliser par `&& false`, `[:0]`, `* 0`, jamais par suppression.
- `feint clean` au milieu du passage complet (oapi-cli.sh:785) supprime
  l'uplink pendant que l'émulateur sert : c'est voulu et ça marche parce que
  les requêtes sont séquencées à ce moment-là. Ne pas « corriger » ça par un
  refus de clean : le garde holder ne vise que les émulateurs vivants.
- Le sandbox de l'agent refuse les heredocs/redirections composées : écrire les
  scripts via Write puis `bash --noprofile --norc <fichier>`.

## Prémisses démenties par la mesure

- « L'échec dépend de l'état accumulé par des runs antérieurs » : **non** — il
  se reproduit depuis une station vierge, au premier passage complet. Ce qui le
  produit est le `feint clean` de fin d'oapi-cli + le parallélisme de tofu.
  L'accumulation (les 7 routes) est réelle mais c'est un défaut **frère**
  (restes), pas le déclencheur du crash.
- « La même jambe isolée passe » s'explique par les restes gardés
  (fnt-default et l'uplink survivent d'un run à l'autre), pas par une
  différence d'arbre.
- L'issue #342 dit « an unmanaged bridge » : exact, et la conséquence non
  écrite est que **le pont survivant ne porte plus d'étiquette** — c'est
  pourquoi clean ne peut pas le supprimer (mustOwn n'a plus rien à lire) et se
  contente de le nommer.

## État de la station à l'instant du handover

Laissé par le passage baseline (le passage a échoué puis la suite tofu a
détruit ses ressources ; l'émulateur est arrêté, aucun processus feint) :

- `feint-uplink` (bridge, étiqueté feint) avec `ipv4.routes =
  10.209.84.0/24,10.70.2.0/24` — **10.70.2.0/24 est un reste** (le subnet azb a
  été détruit par le cleanup tofu, l'arbre commité ne portait pas encore le
  retrait) ; il illustre exactement le défaut corrigé.
- `fnt-default` (ovn, étiqueté) — reste volontairement gardé (politique
  existante).
- dnsmasq de `feint-uplink` (celui du runtime, normal tant que l'uplink vit).
- Aucune machine, aucun pont orphelin, pas de dnsmasq orphelin.
- `feint clean --vm incus-ovn` remettrait tout à zéro ; il a été laissé en
  l'état **exprès** pour que le successeur puisse voir l'adoption
  (`adoptUplink`) retirer 10.70.2.0/24 en vrai au premier
  `FEINT_VM=incus-ovn mise run serve`.
- Scratchpad : `…/scratchpad/` contient les logs de mesure et
  `incus-src` (clone shallow d'Incus v7.2.0, lecture seule).


---

## Suite donnée par le coordinateur (2026-08-20, après épuisement du crédit)

Rebase sur `d735385` (#337) fait, `prepush` vert, entrées de changelog écrites
dans les deux langues. Puis le critère d'acceptation de #341 a été lancé, et il
a produit un résultat que ce brief ne pouvait pas prévoir.

**Le correctif de #341 marche, et c'est mesuré des deux côtés :**

| | référence `d735385` | avec ce lot |
|---|---|---|
| `Failed deleting nftables chain` | **1** | **0** |
| suite `outscale-tofu` | morte dessus | passée |
| stacks d'exemple atteintes | **jamais** (`stacks=0`) | oui |

Vérifié sur tous les logs de passage OVN du scratchpad : `stacks=0` partout.
**`pass-1.log` est le premier passage `incus-ovn` de ce dépôt à atteindre les
stacks d'exemple.** Le mur tombé en révèle donc un autre, exactement comme
#335 avait masqué #341.

**Le mur suivant**, et il n'est pas de notre fait : la stack d'exemple Scaleway
échoue *immédiatement* sur `init and apply`, sur **les deux** serveurs —
`scaleway_instance_private_nic.web[0]` et `web[1]` — avec `the server is
already attached to this private network`. Pas de `Still creating`, donc pas
un timeout ni un retry lent. `run_stack` copie la stack dans un `mktemp -d`,
donc pas d'état résiduel. Et la même stack passe en mode `incus` simple (la
conformance de #337 était entièrement verte). C'est donc un défaut **propre au
mode OVN**, invisible jusqu'ici faute d'y arriver.

Filé séparément plutôt que traité ici : le corriger demande d'instrumenter un
passage OVN de plus, et il ne conditionne pas la valeur de ce lot.

**Conséquence sur le critère de #341** : « deux passages complets verts
d'affilée » n'est pas atteignable tant que ce nouveau mur tient. Ce qui est
prouvé, et qui était la question de l'issue, c'est que la course nftables ne se
produit plus — mesurée présente sur la référence, absente ici, dans le même
passage complet.
