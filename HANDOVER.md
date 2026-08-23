# Reprise du travail sur feint

> Écrit le 2026-08-23. Ce fichier est le point d'entrée unique pour reprendre :
> l'état du dépôt d'abord, le détail du dernier lot ensuite.

## En une minute

`main` est propre et à jour, tout ce qui a été fusionné y est. Aucun agent ne
tourne. Une seule pull request est ouverte : **#418**, qui met à jour ce fichier.

**La prochaine chose à faire est #398**, et sa cause est déjà trouvée : elle est
écrite plus bas, section « La cause du non-déterminisme de `behaviour` ».

L'ordre complet de la 0.11.0 est figé dans `docs/roadmap.md` et dans la
description du jalon. Il ne dépend plus d'aucune conversation.

## L'état du dépôt, mesuré

**Distant** : `main`, `wip/172-peering`, et la branche de la pull request en
cours. Rien d'autre.

**Local** : une quarantaine de branches, et la mesure du 2026-08-23 les répartit
en deux tas seulement.

**Tas 1, sans valeur : 33 branches `worktree-agent-*`**, résidus d'agents. Leur
contenu est intégralement dans `main`. Suppression sans perte.

**Tas 2, à regarder avant de supprimer.** Ces branches portent des commits que
`git` ne voit pas dans `main` parce que les fusions ont été faites en squash,
donc les SHA diffèrent. **Leur contenu, lui, est arrivé sur `main` par d'autres
pull requests**, et deux vérifications le prouvent :
`internal/core/serialise/serialise.go` et `internal/core/resource/resource.go`
existent sur `main` alors que ce sont les sujets de `refactor/serialise-keyed-locking`
et `refactor/resource-new`.

Sont dans ce cas : les cinq `refactor/*`, les trois `feat/172-*`, `fix286`,
`fix292`, `release/0.10.0`, `feat/354-outscale-corpus`, `feat/402-evidence-axes`,
`fix/386-teardown-race`.

### La seule exception, et elle est importante

**`wip/172-peering` porte quatre fichiers qui n'existent nulle part ailleurs :**

- `tools/conformance/parity.sh` — environ 300 lignes qui vérifient qu'une même
  requête produit la même machine chez les trois fournisseurs, **en lisant
  l'hôte plutôt qu'en interrogeant l'API examinée** ;
- `internal/core/machine/incus_fallback_test.go` ;
- `tools/falsify/specs/fallback-retirement.json` ;
- `tools/falsify/specs/ovn-uplink-isolation.json`.

Plus trois commits sur l'appairage de Nets. Le tout a été commité en urgence le
2026-08-21, quelques minutes avant un redémarrage forcé. Le travail s'est arrêté
là.

**C'est suivi par l'issue #419.** Ne pas supprimer cette branche pendant un
ménage : c'est la seule où cela ferait perdre quelque chose.

### Quatre worktrees encore montés

`.claude/worktrees/172-dhcp`, `172-driven`, `172-peering` et
`agent-ac06bc2d90eae7172`. Le dernier pointe sur une branche déjà fusionnée. Ils
se démontent par `git worktree remove`, mais celui de `172-peering` porte le
travail ci-dessus : le retirer avant que #419 soit traitée demande de garder la
branche.

## Les jalons

| jalon | état | thème |
|---|---|---|
| 0.11.0 | 14 ouvertes, 26 fermées | *Observed coverage* — l'ordre est figé dans `docs/roadmap.md` |
| 0.12.0 | 8 ouvertes | *Failure is part of the API* |
| 0.13.0 | 7 ouvertes | *The cloud lab lives in the repository* — `feint.yaml`, `up`/`down`, `verify` |

## Ce qu'il faut savoir avant de toucher au code

Trois gardes mordent systématiquement et coûtent du temps si on les découvre en
route :

1. **Le gel de surface CLI** refuse tout drapeau nouveau tant que
   `cliSurfaceVersion` n'est pas incrémenté et la fixture régénérée par
   `mise run frozen:update`.
2. **`TestTheHelpNamesEveryFlagTheBinaryAccepts`** refuse un drapeau que
   `feint --help` ne nomme pas.
3. **Le harnais de falsification exige que tout nom du fragment survive dans le
   remplacement**, commentaires compris. Neutraliser une condition
   (`… && false`), ne jamais supprimer le terme.

Et un piège d'environnement : `mise run lint` échoue sur un cache golangci-lint
empoisonné dès qu'un worktree voisin disparaît. `golangci-lint cache clean`
règle le problème, ce n'est jamais le code.

## Le détail du dernier lot

Ce qui suit a été écrit à l'arrêt du lot #402 + #398 + #399 + #395, et garde
toute sa valeur pour les deux issues non traitées.

## Changement de jalon, signale pendant le travail

**#398 et #399 sont passees en 0.11.0** (elles etaient sans jalon), parce qu'un
axe dont la valeur oscille et deux falsifications mortes rendent douteux les
chiffres de la release qu'on s'apprete a taguer. **#395 reste en 0.12.0.** #402
est en 0.12.0.

Consequence sur la priorite de reprise : **#398 d'abord**, c'est la seule des
quatre qui reste entierement a faire et elle bloque une release.

## La premiere commande a taper

```bash
cd /home/bob/Projets/coucou/.claude/worktrees/402-evidence
mise trust && mise run prepush
```

Elle doit rendre **0**. Le worktree a du etre `mise trust`e une fois : sans ca
`mise` refuse le repertoire et l'echec ressemble a du code casse.

Puis, pour voir l'outil livre :

```bash
./feint coverage --evidence coverage/evidence.json
```

Elle doit imprimer le tableau de la section suivante, exactement.

## Etat des quatre issues

### #402 : LIVREE, prouvee

Commit `5c06280`. `feint coverage --evidence <record>` rend par fournisseur les
operations servies et le compte plus le pourcentage sur les sept axes.
`--axis <nom>` liste les operations a zero sur cet axe (la file de travail),
`--provider <nom>` restreint les lignes sans jamais restreindre la ligne `all`,
`--format json` publie les memes chiffres. Hors ligne : lit l'artefact commite et
monte les packs en processus, n'ouvre aucune socket. Codes de sortie 0 et 1.

Le bloc genere demande par le coordinateur **est fait** : marqueurs
`<!-- axes:start -->` / `<!-- axes:end -->` dans `docs/routes.md`, rendu par
`internal/cli/docs_axes.go` sur le modele de `<!-- contracts:start -->`, tenu par
`feint docs --check` donc par `prepush` et par le crochet pre-commit. Il porte la
ligne d'avertissement `docsGenerated`, les trois commandes qui le reproduisent en
prose, une ligne par axe disant ce qui le gagne, et la phrase sur les fautes
injectees.

`cliSurfaceVersion` passe de 12 a 13, `internal/cli/testdata/frozen/cli.json`
regenere, ligne CHANGELOG ecrite.

Falsification : `tools/falsify/specs/evidence-axes.json`, cinq mutations,
**les cinq ont mordu au premier essai**, dont celle que l'issue nomme (ranger
toutes les operations sous un seul cloud).

### #399 : LIVREE, prouvee

Commits `98a11c3` et `dc27ef3`. Les deux falsifications mortes sont reparees **au
niveau de la garde**, pas en reecrivant la spec pour la faire passer.

### #398 : PAS COMMENCEE, mais la cause est trouvee et ecrite ci-dessous

Aucune ligne de code. La section « La cause du non-determinisme » est la partie
qui coute cher a retrouver, elle est complete.

### #395 : PAS COMMENCEE

Aucune ligne de code. L'enquete est faite, elle est resumee plus bas, et elle a
trouve **trois collisions et non une**.

## Ce que j'ai mesure

### Le tableau, et il ne differe pas du tien

```
370 operations served, from coverage/evidence.json (machines: incus, none)

provider   served    driven    probed  contract dataplane     shape behaviour  negative
exoscale      104   85  82%   96  92%   95  91%   85  82%   14  13%   78  75%   10  10%
outscale       93   93 100%   93 100%   93 100%   93 100%   23  25%   77  83%   66  71%
scaleway      173  166  96%  141  82%  141  82%  166  96%   15   9%  157  91%   97  56%
all           370  344  93%  330  89%  329  89%  344  93%   52  14%  312  84%  173  47%
```

Servies 173 / 93 / 104, totaux 370 servies, `driven` 344, `negative` 173,
`behaviour` 312. **Identique au tableau du brief, cellule par cellule.** Le
script jetable etait juste.

Un detail qui a failli faire croire l'inverse : les pourcentages doivent etre
**arrondis au plus proche, pas tronques**. 166 sur 173 vaut 95,95 %, ce qui
tronque a 95 et arrondit a 96. Trois des douze cellules du tableau de reference
en dependent. `percentOf` fait l'arrondi en arithmetique entiere et
`TestAPercentageIsRoundedToNearestNotTruncated` epingle les neuf cas mesures.

### Trois sources independantes s'accordent

- les packs montes en processus : 370 operations distinctes (pour **372 routes**,
  deux operations sont servies par deux routes) ;
- `coverage/evidence.json` : 370 entrees ;
- `coverage/<fournisseur>-coverage.json` : 173 + 93 + 104 = 370, disjoints.

Aucune operation orpheline dans un sens ni dans l'autre.

### La cause du non-determinisme de `behaviour` (#398)

**C'est une course entre goroutines, pas un ordre de map.**

`internal/core/emulator/assert.go:159-171`, `soleClientFlightLocked` :

```go
func (o *observer) soleClientFlightLocked() string {
	found := ""
	for _, f := range o.flight {
		if f.synthetic {
			continue
		}
		if found != "" {
			return ""
		}
		found = f.operation
	}
	return found
}
```

Appelee au seul endroit qui compte, `assert.go:206`, pour donner une operation a
un `spanTouch`. L'evenement du store (`internal/core/store/store.go:43-49`) ne
porte que `Action, Provider, Kind, ID` : **aucun nom d'operation**, il faut
l'inferer. Quand deux requetes clientes sont en vol, l'inference rend `""` et
`lifecycleOperations` jette la touche (`assert.go:300-302`).

Donc : `behaviour` est gagne ou perdu selon qu'une requete etait, a la
microseconde ou son evenement de store est parti, la seule en vol. C'est une
piece qu'on lance.

Ce qui l'amplifie, et qu'il faut savoir avant de toucher quoi que ce soit :

- les suites de conformance ne tournent **pas** en parallele entre elles
  (`mise.toml`, tache `conformance`, script sequentiel). Le client parallele,
  c'est **Terraform**, avec son `-parallelism=10` par defaut (aucun `-parallelism`
  nulle part dans le depot), et le span `behaviour` entoure **tout** le cycle de
  vie Terraform : `tools/conformance/scaleway/terraform.sh:84` a `:282`, 22
  blocs `resource` ;
- `mise run evidence:update` lance `conformance` **deux fois** (machines off puis
  on) et fait le OU des deux jambes (`internal/cli/evidence.go:283`). L'artefact
  commite est donc l'union de deux series de lancers de piece ;
- pourquoi `vpcgw/v2/API.GetIP` precisement : la suite `scw` sequentielle ne fait
  jamais `scw vpc-gw ip get`, donc cette operation n'a **aucun** chemin
  deterministe vers l'axe. Son seul pilote est le client parallele.

Pourquoi `negative` est reproductible et `behaviour` non : `negative` est
alimente par `spanExchange` (`conformance.go:229`) ou le nom de l'operation est
**connu**, c'est la route servie sur cette goroutine. `behaviour` doit le
deviner. La regle d'attribution est volontairement conservatrice (elle ne
sur-affirme jamais), ce qui la rend juste mais pas stable.

**Le trou de garde, confirme** : `runtimesLost` (`internal/cli/evidence.go:73-96`)
ne compare que `Machines`, jamais `Operations`. Le commentaire de
`evidence.go:186-190` dit explicitement que le retrecissement d'un axe n'est pas
garde. Rien ne refuse donc une regeneration qui perd des operations sur un axe.
`TestEvidenceRefusesToNarrowTheRuntimesItWasEarnedUnder` ne teste que la
dimension runtime.

**Aucun test de determinisme n'existe** : les six tests de `assert_test.go` sont
tous mono-goroutine, aucun n'exerce le vol concurrent.

### #395 : trois collisions, pas une

L'issue en decrit une. Il y en a trois, et les deux autres ne sont classees nulle
part :

1. **la classee** : `ami-00000001..3` (`internal/providers/outscale/catalog.go:234-236`)
   dans l'espace que le mint produit (`%08x`, `internal/corpus/corpus.go:608-633`).
   **Et aussi `pl-00000001..7`** au meme endroit, lignes 397-403, meme espace,
   jamais signalees ;
2. **Exoscale, espace UUID** : `defaultSecurityGroupID =
   "00000000-0000-4000-8000-000000000001"`
   (`internal/providers/exoscale/securitygroups.go:26-29`) est **octet pour
   octet** la premiere valeur que `mint.synthesise` distribue pour un UUID. Plus
   probable de tirer que la collision `ami-`, puisque c'est le compteur 1 ;
3. **Scaleway, espace IPv6** : `lbV6Block = "2001:db8:0:1::/64"`
   (`internal/providers/scaleway/loadbalancer.go:59-62`) est **inclus** dans
   `syntheticV6 = 2001:db8::/32` (`internal/corpus/corpus.go:717`). Toute adresse
   IPv6 de load balancer que cet emulateur distribue est dans l'espace du
   sanitiseur.

Le compteur `prefixed` est **partage entre tous les prefixes**, pas un par
prefixe : dans `corpus/outscale/oapi-cli-refusals.jsonl` la suite hexadecimale
court de 1 a 0x11 sur seize prefixes differents (`igw-0000000a` le prouve). Donc
`ami-` entre en collision quand il se trouve etre la 1re, 2e ou 3e valeur
prefixee de l'enregistrement. Ici c'etait la 2e.

Le predicat existe deja et est exporte : `corpus.Minted(s)`
(`internal/corpus/scan.go:159`). Il rend `true` sur les quatre valeurs ci-dessus,
verifie a la main. **Toute la difficulte est la couture** : ou obtenir la liste
des valeurs qu'un pack epingle.

La couture a copier existe : `emulator.Vocabulary` + `UnsafeVocabulary`
(`internal/core/emulator/vocabulary.go:36-91`), declaration sur le pack, garde
dans le noyau, test de cablage dans `internal/cli` qui monte les trois packs
(`TestThePacksVocabularyPassesItsOwnGuard`, `sanitise_test.go:224-238`). Un
`emulator.Fixtured { Fixtures() []string }` sur ce modele couvre les trois
collisions, la ou un parcours des catalogues declares n'attraperait que la
premiere (Outscale ne declare que `ReadVmTypes` et `ReadImages`).

**A verifier avant de s'engager, non fait** : `internal/core/emulator` devrait
importer `internal/corpus`, qui importe deja `internal/contract`,
`internal/core/sshkey`, `internal/trace`, `internal/transcript`, `internal/shape`
et `internal/proxy`. Le graphe d'import n'a pas ete construit. Si ca fait un
cycle, la garde va dans `internal/cli` ou les predicats descendent dans
`internal/shape`.

**Piege operationnel** : les deux entrees `#395` de `corpus/accepted.json`
(lignes 550-568) sont porteuses aujourd'hui. Le jour ou la collision disparait,
elles n'excusent plus rien et `corpus --check` sort en 1 sur la garde
« exemption perimee » (`internal/cli/corpus.go:409-424`). **Elles doivent partir
dans le meme commit que le correctif.** Les deux entrees `#392` juste au-dessus
(lignes 538-549) portent sur `CreateVms` de la meme ligne 7 du meme
enregistrement : elles peuvent bouger aussi.

## Ce que j'ai ecarte, et pourquoi

**#398, attribution exacte par le contexte de requete.** `store.Observe`
documente que le callback tourne **sur la goroutine qui a fait la touche**
(`store.go:60-63`), donc l'operation pourrait etre portee par le contexte au lieu
d'etre inferee, et `soleClientFlightLocked` disparaitrait. C'est la bonne
reponse sur le fond et elle **renforcerait** la propriete « ne jamais
sur-affirmer » au lieu de l'affaiblir. **Ecartee pour ce lot** : il y a
**593 sites d'appel** `Store.{Get,Put,Delete,List,Commit}` dans
`internal/providers/`, tous devraient prendre un `ctx`. C'est un diff enorme sur
les trois packs, et ca ferait bouger le chiffre vers le haut (~314-316 stable),
donc ca demande une regeneration de `coverage/evidence.json`, donc une passe de
conformance complete.

**#398, serialiser Terraform (`-parallelism=1`).** L'issue le propose. **A ne pas
faire sans en discuter** : ce parallelisme est ce qui exerce reellement la
concurrence du store, et toute la section « Un effet de bord lent ne tient pas
dans le verrou » de CLAUDE.md existe a cause de defauts que seul un client
parallele revele. On echangerait un chiffre stable contre une couverture perdue.
L'issue mentionne le cout en temps de run, pas celui-la.

**#398, elargir l'attribution.** Non, et l'issue le dit deja : l'axe ne pourrait
plus que sur-affirmer. Ce qui reste jouable est la troisieme voie, « rendre
l'oscillation explicite et bornee » : `spanTouch` retiendrait l'ensemble des
operations en vol au moment de la touche, `lifecycleOperations` rendrait un
second ensemble « peut-etre prouvees », publie a cote de `behaviour`. Cout :
`emulator.ConformanceView` bouge donc surface gelee plus `schemaVersion` plus
`frozen:update`, et `coverage/evidence.json` ne porterait la borne qu'apres une
passe de conformance.

**#399, supprimer les specs mortes.** Explicitement refuse par le brief, et de
toute facon les deux gardes avaient un vrai trou derriere.

**#402, deviner le fournisseur au prefixe du nom d'operation.** C'est ce que
faisait le script jetable. Le proprietaire d'une operation est le pack qui monte
une route la declarant, demande en processus. Une table de prefixes serait juste
aujourd'hui et fausse en silence au premier renommage de produit.

**#402, mettre le tableau dans `docs/confidence.md`.** Cette page se termine par
« It is not a coverage percentage, and it deliberately carries no score ». Y
mettre un tableau de pourcentages la contredirait dans ses propres mots.
`docs/routes.md` a ete choisi : la legende longue qui definit chaque axe y est
deja, dans l'autre bloc genere de la meme page, et le tableau est le total des
colonnes des lignes juste en dessous.

## Les premisses du brief que la mesure a contredites

1. **« `operations` est une map nom d'operation vers sept booleens ».** Non :
   **trois des sept sont des verdicts, pas des booleens.** `probed` vaut
   `response` / `refusal` / `none`, `contract` vaut `clean` / `violating` /
   `unchecked`, `shape` vaut `observed` / `unobserved` / `unknown`. Compter
   `probed` par verite compte `none` comme gagne. C'est le piege suivant apres
   celui que le script jetable a rencontre, et c'est pour ca que chaque axe porte
   son propre predicat dans `evidenceAxisList` et que le test de comparaison
   verifie le type JSON de chaque champ.

2. **« Si ta commande rend autre chose, trouve lequel des deux se trompe ».** Ni
   l'un ni l'autre : ma commande rend exactement ton tableau. La seule chose qui
   pouvait faire croire a un ecart etait le mode d'arrondi.

3. **#399, « le test lui-meme ne semble pas lire ce que la mutation change »**
   (a propos de `TestEveryPackRunsTheSharedBarrage`). **Faux.** Il lit bien ce que
   la mutation change. Ce qui a casse, c'est que le pack Exoscale est passe de un
   a trois sites d'appel `storetest.NoLostUpdate(` le 2026-08-18 (#289), et que
   le harnais ne reecrit que **la premiere** occurrence. La garde avait quand meme
   une vraie faiblesse, corrigee separement : elle cherchait une sous-chaine, donc
   une mention en commentaire la satisfaisait.

4. **#399, « la premiere a cesse de mordre parce que la liste d'acceptation a
   fait son travail » (retiree par #388).** Vrai sur le fond, faux sur la date :
   `corpus/accepted.json` portait **zero** entree de genre `value` ou `order` deja
   a `1f971e3`, **le commit meme qui a cree la spec**. Le retrait a eu lieu
   **a l'interieur** de la PR #388, apres l'ecriture du label. **Cette
   falsification n'a jamais mordu sur `main`.**

5. **Portee de #399 : « 2 falsifications sur 83 ».** La mesure en trouve **4 sur
   460** avec un fragment ambigu, dans 3 specs. Les deux autres
   (`refusal-corpus.json`, `ssh-suite-needs-its-images.json` deux fois) etaient
   affaiblies de la meme facon sans que personne l'ait vu.

6. **Portee de #395 : « l'assainisseur frappe des identifiants Outscale ».** Trois
   espaces reserves sur trois sont violes, pas un. Voir plus haut.

## Ce qui est casse ou a moitie fait

**Rien n'est casse.** `mise run prepush` et `./feint corpus --check` sortent tous
les deux en 0, verifie en capturant le code de sortie avant tout tube (`mise run
prepush | tail` mesure `tail`, pas la tache).

A moitie fait, nomme sans euphemisme :

- **`internal/cli/corpus.go`, `noReplayInvariants`** : l'entree `exoscale` est une
  exemption que j'ai ecrite moi-meme parce que le pack ne declare aucun
  `ReplayInvariant` et que personne n'avait ecrit pourquoi. Elle dit que c'est une
  file de travail et non une decision. **Ce n'est pas un correctif, c'est un
  constat rendu visible.** Le vrai travail est de declarer les invariants
  Exoscale ; il n'a pas d'issue a ma connaissance, il en faudrait une.
- **Pas de ligne CHANGELOG pour #399.** Les deux commits touchent des gardes
  internes, pas une forme de reponse ni une limite, donc la regle du fichier les
  laisse a `git log`. A trancher si la 0.11.0 veut annoncer que deux
  falsifications ne mordaient plus.
- **`mise run falsify:all` n'a pas ete rejoue en entier** (des minutes, et l'arret
  est demande). Ont ete rejouees et vertes : `evidence-axes.json` (5 sur 5) et
  `outscale-corpus.json` (3 sur 3). `shared-layer-is-enforced.json` **n'a pas ete
  rejouee apres son reciblage** : `falsify:lint` passe et la garde a ete prouvee a
  la main par le temoin decrit dans le message de commit, mais la replique
  complete reste a faire. **C'est la premiere chose a lancer en reprenant, apres
  `prepush`.**

```bash
mise run falsify -- tools/falsify/specs/shared-layer-is-enforced.json
```

## Etat de la station

Ce qui subsiste et qui est a moi :

- le worktree `/home/bob/Projets/coucou/.claude/worktrees/402-evidence` sur
  `feat/402-evidence-axes`, 3 commits locaux, arbre propre, **non pousse** ;
- le binaire `./feint` reconstruit dans ce worktree (ignore par git) ;
- des fichiers temporaires dans le scratchpad de la session
  `/tmp/claude-1000/-home-bob-Projets-coucou/642e21ac-.../scratchpad/`, tous
  prefixes `402-`, dont un binaire `402-feint-mutated`. Rien d'utile, effacables.

Ce que j'ai touche hors du worktree : **rien**. Aucun conteneur, aucun reseau,
aucun processus laisse en vie, aucune suite longue lancee. `mise trust` a ete
accorde a ce worktree, c'est la seule modification de configuration.

Le fichier `internal/cli/zz402_scratch_test.go` a existe pendant l'exploration et
a ete **supprime** : un fichier non suivi dans `internal/cli` est copie par le
harnais de falsification et peut invalider une replique.

---

## Ajout du 2026-08-22, après l'arrêt de l'agent : #408 livrée

Une cinquième chose est sur cette branche, faite après le brief de reprise
ci-dessus. Elle n'était pas dans le lot d'origine.

### Ce qui est livré

**#408 — la file de travail.** `feint coverage --evidence <record> --gaps`
répond, par cloud et par axe, aux opérations à zéro **et au travail que chaque
zéro nomme**. C'est le complément de #402 : le score dit où l'on en est, la file
dit quoi faire ensuite.

Fichiers : `internal/cli/evidence_gaps.go`, `evidence_gaps_test.go`,
`tools/falsify/specs/evidence-gaps.json`. Le drapeau est branché dans `cli.go`
à côté de `--evidence`, décrit dans le bloc d'aide de `coverage`.

Quatre natures, **toutes dérivées du registre** et jamais d'un nom ou d'une
liste tenue à la main : `violating` (défaut du pack), `unrecorded` (piloté,
jamais confronté à une vraie réponse), `undriven` (aucun client ne l'atteint),
`unproven` (le registre n'explique pas). La dernière est nommée plutôt que
fondue dans une voisine — un seau qui absorbe l'inexpliqué est la façon dont une
file se met à mentir.

Ordre : par nature, puis par nom. Déclaré, pas calculé. **Aucun pourcentage
cible nulle part.**

### Ce que la mesure a donné

```
                suites à écrire   enregistrements   inexpliqué
  exoscale            111                71             83
  scaleway             36               151            141
  outscale              0                70             43
```

Exoscale a surtout besoin de suites de conformance, Scaleway d'enregistrements.
Deux métiers différents, que le score seul ne pouvait pas nommer.

### Trois choses qui ont mordu, et qu'il faut connaître avant de toucher à ça

1. **Le gel de surface CLI** refuse tout drapeau nouveau tant que
   `cliSurfaceVersion` n'est pas bumpé (13 → 14 ici) et la fixture régénérée par
   `mise run frozen:update`.
2. **`TestTheHelpNamesEveryFlagTheBinaryAccepts`** refuse un drapeau que
   `feint --help` ne nomme pas : « un drapeau que seuls la source et
   `coverage --help` connaissent est un drapeau qui a livré muet ». Le bloc
   d'aide de `coverage` est dans `cli.go`, vers la ligne 318.
3. **Le harnais de falsification exige que tout nom du fragment survive dans le
   remplacement** — commentaires compris. Un terme supprimé qui orpheline un nom
   fait échouer tout le paquet, ce qui se lit exactement comme la garde prouvée.
   Neutraliser la condition (`… && false`), ne jamais supprimer le terme. C'est
   la correction que #399 vient d'apporter sur cette même branche, et elle a
   servi le jour même.

### Une mutation restée verte au premier essai

Elle nommait une faiblesse de la **fixture**, pas du code : aucune de ses
opérations n'était orpheline de pack, donc la garde qui écarte une telle
opération pouvait être retirée sans qu'une assertion bronche. Une opération
`nobody/v1/API.Orphan` a été ajoutée, et la mutation mord. Les six mordent
maintenant.

### Ce que ça a produit en aval, et qui n'est pas sur cette branche

Sept issues et un jalon, créés depuis la sortie de `--gaps` :

- **jalon 0.14.0 — *Every cloud is proven the same way***
- **#409** machines · **#410** réseau privé · **#411** stockage bloc ·
  **#412** répartiteur · **#413** groupe de sécurité · **#414** adresses IP
- **#415** les 44 opérations qu'aucun domaine ne rangeait

#415 porte une question qui concerne directement cette branche : **le
classificateur de domaines vit dans un script jetable**. S'il devient la façon
de piloter la parité, il appartient à côté de `--gaps` avec un test qui échoue
quand une opération ne correspond à aucun domaine — sinon le prochain jalon
s'écrira sur une carte partielle. C'est le motif que ce dépôt a déjà payé deux
fois cette semaine.

### État des portes

`mise run prepush` vert, `docs:check` vert, `tools/falsify/specs/evidence-gaps.json`
six mutations rouges. Rien poussé, rien de rebasé depuis.
