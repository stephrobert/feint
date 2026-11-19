# Contribuer à Feint

**Autre langue :** [English](./CONTRIBUTING.md)

## La règle unique

**Un changement de surface émulée n'est pas terminé tant qu'un vrai client ne
l'a pas exercé.**

Les tests unitaires affirment ce que nous croyons qu'une API fait. `scw`,
`terraform` et les SDK officiels affirment ce qu'elle est réellement. Lancez
`mise run conformance` et dites-le dans la pull request ; si une suite n'a pas pu
tourner localement, indiquez-le explicitement.

## Démarrer

```bash
mise install          # Go, Python, uv et les linters, versions épinglées
pre-commit install    # les gates locaux ; voir plus bas, celui-ci n'est pas optionnel
mise run check        # ce que la CI lance sur une pull request
mise run serve        # l'émulateur sur 127.0.0.1:4599
```

**`pre-commit install` est une vraie étape, pas une politesse.** Les hooks git
vivent dans `.git/hooks/`, qui n'est pas versionné : un clone frais n'en a aucun,
si complet que paraisse `.pre-commit-config.yaml`. Sans cette commande, les
contrôles qui imposent les règles propres à ce projet ne tournent jamais sur votre
machine : qu'une route déclare son opération upstream, qu'aucune dépendance
externe ne se soit glissée dans `go.mod`, que la documentation générée corresponde
encore aux artefacts qu'elle décrit, qu'une réponse corresponde encore à la
description d'API du provider. La CI attrape tout cela, mais des heures plus tard
et devant tout le monde.

Elle installe trois types de hooks, et les deux derniers se ratent facilement :
`pre-commit`, `commit-msg` et `pre-push`. La configuration déclare les trois via
`default_install_hook_types`, donc la commande seule suffit.

### Avant de pousser

Le hook `pre-push` lance `mise run prepush` : `check`, `docs:check`,
`drift:check`, `shapes:check`, `corpus:check`, `falsify:selftest`,
`falsify:lint`, `release:surface` et `testplan:check`.
Déterministe, hors ligne, quelques secondes.
C'est ce qu'une pull request refusera, sans jamais dépendre de la météo d'un
runner. Un hook n'est pas un portillon : un clone neuf n'en a aucun, et
`--no-verify` le saute, donc chacune des neuf est aussi une étape d'un workflow
déclenché par toute `pull_request`, dans un job et une étape qui ne portent
ni `if:` ni `continue-on-error`. Une exception, lue sous une seule écriture :
`drift:check` tourne dans une étape `continue-on-error`, et l'étape suivante
fait échouer la pull request quand elle lit que la première a échoué.
`TestEveryPrepushTaskRunsOnAPullRequest` (`tools/ci/falsify_harness_test.go`,
#936) échoue quand une tâche entre dans `prepush` sans qu'aucune pull request la
lance.

**Ce n'est pas suffisant**, et ce qui manque dépend de ce que vous avez changé.
Vous n'avez pas à vous en souvenir — demandez :

```bash
mise run testplan          # ce que ce diff a mérité, du moins cher au plus cher
```

Il lit le diff, imprime les exécutions, et imprime ce qu'elles ne prouvent
**pas**. Il ne lance rien lui-même : le choix est la moitié qu'un humain doit
pouvoir contester. `mise run prepush` l'appelle avec `--check`, donc chaque
poussée dit quoi jouer ensuite et refuse un chemin qu'aucune règle ne trie —
« ça ne correspond à rien, donc on ne joue rien » est le défaut bon marché, et
un chemin non trié est une absence, pas une décision (#564).

Il imprime aussi **les falsifications que le diff a méritées**, lues dans
`tools/falsify/specs/` plutôt que retenues : 274 specs nomment 341 fichiers
distincts en 2402 mutations, et une spec qui mute un fichier que vous avez
touché est le sous-ensemble de `falsify:all` que votre changement peut avoir
empêché de mordre. Chaque lot d'ici a suivi cette discipline à la main et l'a
écrit dans sa pull request, ce qui veut dire qu'il l'a aussi oubliée à la main :
`mise run check` ne voit pas une garde dont le test passe encore, au-dessus de
rien.

Ces trois chiffres ne sont pas retenus non plus. `mise run check` les recompte
sur les specs et refuse cette phrase dès que l'un d'eux a dérivé
(`tools/testplan/figures.go`, #937). La phrase disait *128 specs nomment 186
fichiers distincts* jusque-là, et les deux chiffres restent côte à côte parce
que l'écart est le sujet : les specs avaient doublé pendant que la phrase ne
bougeait pas, et un chiffre écrit à la main sur une population qui grossit a la
durée de vie de la fusion suivante.

Un fichier `_test.go` ou un répertoire `testdata/` ne mérite aucune jambe de
conformance, pour la raison qui vaut pour la prose : rien de compilé dans les
seuls tests n'atteint un client. Il reste trié, donc un fichier de test dans un
paquet qu'aucune règle ne nomme rougit toujours.

Les coûts sur lesquels il ordonne sont mesurés, une passe chacun le 2026-08-27 :

| exécution | mesure | ce qu'elle porte |
|---|--:|---|
| `conformance:leg -- probe` | **0,7 s** | chaque route montée pilotée depuis sa propre description d'API, sans client |
| `conformance:leg -- exo-cli` | 4,0 s | le vrai `exo` |
| `conformance:leg -- scw-cli` | 7,1 s | le vrai `scw` |
| `conformance:leg -- terraform` | 45,4 s | les deux fixtures Terraform, sur l'un ou l'autre moteur |
| `conformance:leg -- octl` | 141,3 s | le vrai `octl` |
| `conformance:leg -- fields` | 208,1 s | les quatre clients, les fautes, les refus — **la seule jambe où le gate d'omission juge** |
| `conformance:leg -- runtime` | 590 s | les quatre suites qui exigent de vraies machines ; refuse de tourner sans `FEINT_VM` |
| `mise run conformance` | 256 s | tout sauf les suites runtime, qui se sautent elles-mêmes |
| `FEINT_VM=incus-ovn mise run conformance` | **1331 s** | l'ensemble |

**Une décision domine toutes les autres** : le changement atteint-il
`internal/core/machine`. Répondez juste et le travail coûte des secondes ;
répondez faux et il coûte 590 s ou 1331 s. Le reste du tableau vaut des dizaines
de secondes — `fields` à 208 s contre la passe complète sans runtime à 256 s
économise 48 s, c'est-à-dire rien. `testplan` répond donc à cette seule question
en **lisant les imports** des fichiers modifiés, et non en consultant une liste
de noms de répertoires, qui est la moitié qu'une table se tromperait en premier.

**Le plan remplace la passe complète en local. Il ne remplace jamais la matrice
CI.** Tous les autres gates d'ici ajoutent de la preuve ; celui-ci en retire, et
cela inverse le mode de défaillance : une règle fausse est silencieuse. La CI
inchangée, le pire cas d'une règle fausse est une pull request rouge, ce pour
quoi la CI existe.

**Chaque plan imprime sa propre limite, et ce n'est pas une politesse.** L'outil
s'est trompé cinq fois en une semaine (#588). Quatre fois du côté cher, ce qui
est survivable ; la cinquième, il a nommé quatre exécutions et 27 specs, toutes
vertes, alors que la jambe qui reproduit le défaut n'était dans aucune. Pris
pour un plafond, ce plan disait que le travail était fait. Un plan se termine
donc en nommant ce qu'aucune table ne peut savoir : **dans quelle population
vit le défaut**. Quatre pourritures mécaniques sont gardées dans
`tools/testplan/plan_test.go` : un chemin non trié, une règle qui ne correspond
à rien, une phrase `Unproven` que l'artefact qu'elle nomme contredit, et une
règle qui prescrit une jambe incapable de piloter ce qu'elle trie. L'erreur de
population n'est gardée par rien, et c'est écrit plutôt que dissimulé.

La table qu'il lit, sous forme courte :

| ce que votre changement touche | à lancer en plus | ce que cela prouve |
|---|---|---|
| une route, une forme de réponse, une erreur | `mise run conformance` | qu'un vrai client passe encore |
| un gate, un score, un verdict sur une exécution | `mise run conformance:leg -- <jambe>` | qu'il tient sur une exécution **partielle** |
| une garde, une validation, un refus | `mise run falsify -- <spec.json>`, puis la déclarer dans `tools/falsify/specs/` | que le test échoue sans elle, les jours suivants aussi |
| `internal/core/machine`, un réseau, un pare-feu | `FEINT_VM=incus-ovn mise run conformance:leg -- runtime` pendant le travail, `FEINT_VM=incus-ovn mise run conformance` une fois avant de pousser | que le runtime accepte ce que le pilote envoie |
| un artefact de `coverage/` | `mise run evidence:update` | que le registre n'a pas rétréci |

**La deuxième ligne est celle qui a coûté deux pull requests rouges.**
`mise run conformance` pilote toutes les suites contre un seul émulateur, ce qui
est exactement la population qu'un verdict du genre *aucune réponse de cette
exécution n'a porté ce champ* suppose : un gate qui juge une exécution y est vert
**par construction**. La CI ne fait pas cela. Le workflow de conformance
répartit les clients en matrice, un émulateur par jambe, si bien que toute jambe
sauf `fields` est une exécution partielle : la jambe `probe` ne pilote aucun
client, et la jambe `terraform` ne pilote pas `octl`.

`mise run conformance:leg -- probe` reproduit une de ces jambes en local, et la
reproduction est prouvée plutôt que supposée : en remettant la garde qui
manquait, la jambe nomme localement les mêmes champs que le job en échec avait
listés. `TestEveryMatrixLegCanBeReproducedLocally` tient désormais les deux
listes ensemble, de sorte qu'une jambe renommée dans la matrice ne peut pas
rester irreproductible.

**La jambe de la quatrième ligne s'appelle `runtime`, et ce n'est pas une entrée
de la matrice.** Les quatre suites qui exigent de vraies machines appartiennent à
`runtime-proof.yml`, pas à la matrice de conformance, et jusqu'à #459 rien ne les
reproduisait sinon `FEINT_VM=incus-ovn mise run conformance` en entier : 1331 s
mesurées le 2026-08-27, dont 675 s pour ces quatre suites et 656 s de suites
clientes devant elles dont un changement de la couche machine n'avait aucun
besoin. La jambe elle-même a mesuré 590 s sur la même station, d'un portillon à
l'autre.

```bash
FEINT_VM=incus-ovn mise run conformance:leg -- runtime
```

Elle refuse de tourner sans runtime plutôt que de laisser ses suites se sauter
elles-mêmes : une jambe demandée par son nom qui ne mesure rien ne doit pas
rendre un succès.

La forme générale, qui vaut ailleurs : **un contrôle dont le verdict porte sur
« cette exécution » doit être éprouvé sur l'exécution la plus pauvre qui le
déclenchera, jamais sur la plus riche.** La plus riche est celle qu'on a sous la
main, et c'est ce qui rend l'erreur si facile.

### Falsifier une garde

Un test qui passe ne prouve rien par lui-même : il peut affirmer quelque chose
qui était déjà vrai. `mise run falsify -- tools/falsify/specs/<nom>.json` retire
la garde dans une copie **hors** du dépôt et exige que le test nommé rougisse.

Il refuse une forme de mutation d'emblée, avant toute copie, et la raison est
mesurée plutôt que théorique. Le 2026-08-15, la même erreur a annulé un verdict
**quatre** fois : trois dans trois issues sans rapport, et une quatrième trouvée
le même jour en faisant tourner le harnais de bout en bout, plutôt qu'en
raisonnant dessus (#187, commit `b39e4af`).

```go
if ok && fe.EnforcesFirewall() {   ->  if ok {        // fe devient inutilisée
if strings.TrimSpace(s) == "" {    ->  if false {     // s  devient inutilisée
if err != nil || n == 0 {          ->  if n == 0 {    // err devient inutilisée
```

La quatrième est la même erreur, cachée de la règle qui attrape les trois
premières : quand le fragment porte aussi la déclaration, le nom apparaît des
deux côtés et une différence d'ensembles ne voit rien disparaître.

```go
fe, ok := p.(FirewallEnforcer)     ->  fe, ok := p.(FirewallEnforcer)
if ok && fe.EnforcesFirewall() {       if ok {        // fe reste inutilisée
```

Go refuse de compiler une variable inutilisée, donc chaque mutation cassait la
compilation. Or une mutation qui ne compile pas fait échouer **tous** les tests,
ce qui ressemble exactement à une garde prouvée. Donc : **ne jamais supprimer le
terme qui mentionne un nom.** Neutralisez la condition en gardant chaque nom
évalué :

```go
if ok && fe != nil {
if strings.TrimSpace(s) == "\x00never" {
if (err != nil && false) || n == 0 {
```

`mise run falsify -- --selftest` tient cette règle contre ces quatre cas réels et
contre leurs corrections, et `mise run prepush` le lance. Les deux moitiés
comptent : une règle qui refuserait toute mutation passerait la première et
rendrait l'outil inutile.

Le « quatre » de cette section est le chiffre que la tâche imprime elle-même,
*the rule refuses all 4 historical mistakes, accepts all 4 fixes*, compté sur la
liste qui les porte, et non un chiffre que cette page retient. Elle écrivait
« trois », ici et dans `mise.toml`, pendant que la tâche imprimait quatre
(#937) : c'est le même défaut qu'un compte périmé, en habit différent. Un chiffre
écrit une fois, cité pour tenir un argument, et recalculé par rien.

**Déclarer la mutation, pas seulement la lancer.** Une falsification prouve qu'un
test mord *le jour où on la lance*, et une exécution unique dans une branche qui
disparaît ensuite ne laisse qu'une affirmation sur le passé. Ce dépôt en a déjà
publié une qui était fausse : le CHANGELOG de 0.8.0 dit que neutraliser
n'importe lequel des trois verrous fait rougir le barrage, et trente exécutions
sont ensuite restées vertes avec un verrou retiré. La mutation va donc dans
`tools/falsify/specs/`, à côté de la garde, et `mise run falsify:all` les rejoue
toutes chaque nuit (`.github/workflows/falsify.yml`).

**Une spec est du code, et elle se relit comme du code.** Elle ressemble à du
JSON, mais le `replace` d'une mutation est du Go que `go test` compile et
exécute, sur la station qui la rejoue et à la nuit suivante. Une spec reçoit
donc la relecture d'un fichier `.go`, et une spec venue de la branche de
quelqu'un d'autre se lit avant que `mise run falsify` y touche. Les deux champs
qui atteignaient `go test` sans ressembler à du code sont fermés au chargement
(#989) : `env` ne peut poser qu'une variable de la liste exacte du harnais
(`SPEC_ENV_KEYS`, aujourd'hui `FEINT_TESTCONTAINER`), et `package` est un chemin
de paquet Go relatif, jamais un drapeau.

Le premier rejeu complet a trouvé quatre specs sur huit qui ne tenaient plus, et
rien de tout cela n'était une pourriture de l'émulateur : trois mutations étaient
écrites dans le style suppressif, une nommait deux gardes qu'une réécriture
ultérieure avait retirées, et une déclarait un test qui ne correspondait pas à la
garde placée dessous. Ce dernier cas a compilé et a laissé le test nommé vert,
c'est celui pour lequel le rejeu existe, et aucun `go test` ne peut le voir.
`mise run falsify:proof` est cette affirmation mesurée : il desserre une
assertion jusqu'à ce qu'elle ne puisse plus échouer, puis exige que le rejeu
rougisse en la nommant pendant que la suite reste verte.

`mise run conformance` n'est **délibérément pas** dans le hook. Il réclame `scw`,
`octl`, `exo` et Terraform installés, et prend des minutes : en hook, il
échouerait sur un binaire absent plutôt que sur votre code, et le réflexe qu'il
enseignerait est `--no-verify`, qui désarme tous les hooks d'un coup. Un gate que
l'on saute par habitude est pire que pas de gate.

### Si vous contribuez depuis un fork

Rien ne tournera sur votre pull request tant qu'un mainteneur ne l'aura pas
approuvée, et l'approbation est redemandée à chaque push. D'ici là, GitHub garde
chaque exécution de workflow en `action_required`. Le dépôt l'applique à tout
contributeur qui n'en est pas membre, pas seulement à un premier contributeur :
le réglage vaut `all_external_contributors` depuis le 2026-11-16. Avant cette
date, il valait `first_time_contributors`. Un compte qui avait déjà une pull
request fusionnée lançait donc la CI sans aucune approbation.

L'approbation empêche du code venu de l'extérieur de tourner sur les runners de
ce dépôt avant que quelqu'un l'ait lu. Le prix est pour vous : `gh pr checks`
répond « no checks reported » et vous n'avez aucun retour, pas même un échec.
Une pull request dont la CI n'a pas démarré n'est pas ignorée. Elle attend un
lecteur.

Les gates locaux ne sont donc pas un confort pour vous, ce sont les seuls dont
vous disposez jusqu'à ce que quelqu'un regarde :

```bash
mise run check         # ce que la CI lance sur chaque pull request
mise run conformance   # les vrais clients, pour tout ce qui touche une route ou une suite
```

Une pull request a déjà été fusionnée ici en cassant la suite de conformance
Exoscale, sans rien sur la page pour le dire. Lancer la suite soi-même est ce qui
l'aurait montré, et c'est pourquoi la checklist des contributions assistées par IA
demande si vous l'avez lancée plutôt que si vous vous attendez à ce qu'elle passe.

Les SDK upstream que lit le scan de dérive sont clonés à la demande :

```bash
mise run upstream:sync
```

## Ajouter de la surface émulée

Le tout, en quatre points :

1. Lire le source du SDK upstream pour les formes exactes, pas la doc web.
2. Tracer le vrai client (`scw -D <commande>`) et émuler la séquence **entière** :
   une création est rarement un seul appel.
3. Déclarer l'opération upstream sur chaque route ; déclarer ce que vous ne servez
   pas dans `Declined()`, avec une raison.
4. Les tests : aller-retour de cycle de vie, pagination, cloisonnement, un par
   forme d'erreur. Fuzzer tout nouveau décodeur de requête.

## Proposer une stack que vous faites déjà tourner

La contribution la plus utile ici n'est pas toujours du code. Une racine
Terraform ou OpenTofu que vous faites tourner en gate obligatoire sur votre
propre infrastructure est une preuve que nous ne pouvons pas fabriquer : elle
change à votre rythme, contre des providers que vous résolvez à neuf, et c'est
comme cela que la rupture du provider Scaleway 2.81.0 nous est parvenue (#325).

Ce que nous demandons d'une telle stack, ce que nous en faisons, et ce que nous
n'en faisons délibérément **pas** — non, nous ne clonerons pas votre dépôt dans
notre CI — est écrit dans
[`examples/stacks/README.md`](examples/stacks/README.md#offering-your-stack-what-we-ask-and-what-we-do-with-it).
Le registre de celles déjà rejouées est
[`examples/stacks/surveyed.md`](examples/stacks/surveyed.md).

## L'upstream a bougé

Le workflow hebdomadaire ouvre une pull request quand l'API du provider change.
La trier est tout le travail : chaque nouvelle opération finit implémentée ou
déclinée avec une raison, jamais en silence, et une route orpheline est
investiguée avant que la baseline ne soit rafraîchie. « Pas encore fait » et
« hors périmètre » sont deux réponses différentes et seule la seconde a sa place
dans `Declined()`.

## Issues

Trois formes, un flux automatisé, une règle.

**Comment naît une issue.** Un client officiel cassé utilise *An official client
did not behave* — ce rapport prime sur toute la roadmap : l'ordre là-bas est une
supposition sur ce dont les gens ont besoin, un client qui casse est un fait. Une
route dont vous avez besoin utilise *An operation is missing*. Un lot de
[docs/roadmap.md](docs/roadmap.md) utilise *Roadmap batch*, une issue par
identifiant de son tableau. Le workflow de dérive hebdomadaire ouvre et met à jour
sa propre issue sous le label `drift` ; personne n'ouvre celle-là à la main.

**Comment se lit un titre.** Deux formes, parce que les issues font ici deux
choses différentes, et les mélanger coûte la possibilité d'en citer une.

Un **défaut** est titré par la phrase qui dit ce qui casse, sans préfixe : *A
stopped Vm outside a Subnet loses its PrivateIp*, *DryRun: false makes the
project's own conformance gate fail*. Un symptôme, pas un diagnostic et pas un
correctif proposé — le diagnostic est souvent faux au moment où l'issue est
ouverte, et le titre lui survit.

Une **unité de livraison** est titrée `<CODE>-<n> : ` puis ce que le lot rend
vrai. Les codes sont `SW`, `OSC`, `EXO` pour un pack, `UI` pour l'interface, `X`
pour tout ce qui les traverse : *OSC-2: ProductCodes, admin password, tags*. Le
code n'est pas une décoration. Les commits ferment des lots en le nommant —
`Closes #6 (OSC-2)` —, docs/roadmap.md ordonne par lui, et le gabarit *Roadmap
batch* a un champ pour lui. **Une issue sans code est une issue qu'aucun commit ne
peut nommer.**

Six issues ont été ouvertes en un après-midi sans code, alors que ce champ de
gabarit existait depuis toujours. Tout ce qu'un titre est censé porter est porté
par qui s'en souvient : lisez donc cette section avant d'ouvrir, pas après.

Une question à trancher n'est ni l'une ni l'autre : elle s'ouvre par `Reopen` ou
`Decide` et énonce la question, parce qu'un titre qui se lit comme un lot invite
quelqu'un à implémenter ce qui n'a pas été décidé.

**Ce qui ferme une issue.** La commande de son champ « closed by », lancée et
verte — jamais une intention. Les lots de roadmap se ferment sur les quatre
conditions « When a batch is done » de docs/roadmap.md, que chaque issue de lot
répète mot pour mot plutôt que de paraphraser.

**Comment se lisent les labels.** Quatre axes, au plus un label par axe. Ce que
c'est : `bug`, `enhancement`, `roadmap`, `drift`. Quel pack : `scaleway`,
`outscale`, `exoscale` — absent veut dire transversal. Quelle couche partagée,
seulement quand elle est le sujet : `core`, `machine`, `conformance`. État :
`blocked`, avec le bloqueur nommé dans le corps. Il n'y a pas de label
`wontfix` : le mot du projet pour « hors périmètre » est un refus dans le pack,
avec une raison.

**Les milestones** sont des thèmes, pas des versions, et elles ne portent pas de
date d'échéance : un ordre, jamais un calendrier. Cette phrase disait « les six
vagues de la séquence de la roadmap ». Ce n'était plus vrai depuis 0.9.0, et la
phrase est restée là trois mois de releases plus tard. C'est ainsi que deux
milestones ont fini titrées d'après des tags publiés, `0.14.0` et `0.15.0`,
avec 19 et 10 issues ouvertes à elles deux.

La règle est donc maintenant vérifiable plutôt que décrite, et elle est plus
étroite qu'il n'y paraît : **une milestone ouverte ne revendique jamais une
version déjà taguée.** Une milestone pour du travail encore à venir peut porter
son numéro. `1.0.0` en est une, et c'est le cas où le tag doit attendre la
milestone, pas l'inverse. Ce qui est refusé, c'est une affirmation sur le passé
que les tags démentent. La seconde moitié est son miroir : la version qu'on
tague ne doit porter aucune issue ouverte sous son propre nom.

`tools/release/milestone-gate.sh` pose les deux questions avant un tag, et le
dit quand il n'a pas pu mesurer, au lieu de refuser pour une raison qui ne tient
pas au dépôt. Dans `tools/ci`,
`TestTheMilestoneGateRefusesAVersionOnAnOpenMilestone` est piloté par le
contenu qui l'a motivé, et `TestTheMilestoneGateAcceptsTheShippedShape` garde
honnête la moitié qui accepte : un portillon qui refuserait tout tag passerait
le premier test et enseignerait `--no-verify`.

`tools/issues/setup.sh` crée les labels et les milestones (`--dry-run` montre ce
qu'il ferait) ; `tools/issues/batches/` contient les dix-huit définitions de lots,
prêtes à devenir des issues.

**Les lots sont pilotés depuis un tableau de projet** — les vues, les champs, et
ce que « prêt à démarrer » veut dire sont dans
[docs/project.md](docs/project.md). Les issues de lot portent des dépendances
blocked-by natives, et le workflow `Unblock` déplace le label `blocked` quand un
bloqueur se ferme ; personne ne l'entretient à la main.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/fr/v1.0.0/), scope par
domaine :

```text
feat(scaleway): emulate flexible IP attach/detach
fix(core): keep list order stable across pages
chore(drift): refresh the upstream baseline
```

**Le sujet n'est pas une préférence de style : la version en est déduite.**
`cz bump` lit les commits depuis le dernier tag et décide de l'incrément — `fix`
fait bouger le patch, `feat` le mineur, un `!` avant les deux-points marque une
rupture, et avant 1.0 cette rupture reste à l'intérieur de `0.x` plutôt que
d'annoncer 1.0.0. Un sujet qui ne s'analyse pas est une release qui calcule le
mauvais numéro, et le numéro est ce que les utilisateurs épinglent.

Vérifié deux fois, par le même outil : le hook `commit-msg` refuse le message au
moment où vous l'écrivez, et le workflow `Commits` vérifie chaque sujet qu'une
pull request ajoute — parce qu'un hook vit dans `.git/hooks/`, qu'aucun clone ne
transporte.

```bash
cz check --rev-range origin/main..HEAD   # ce que la CI dira
cz bump --dry-run                        # la version qu'impliquent vos commits
```

## Qui répond d'une pull request

Trois règles, décidées le 2026-11-16. Elles valent pour tout le monde, quel que
soit ce qui a écrit le code.

### Une personne identifiable en répond

**Une contribution a un auteur identifiable, qui en répond.** La section sur les
contributions assistées par IA, plus bas, le dit déjà : l'humain qui soumet est
l'auteur, et il prend la responsabilité de chaque ligne, licence comprise. Voici
ce qui en découle. Une pull request qu'aucune personne identifiable ne peut
porter est fermée, même si son code est correct. Le défaut qu'elle visait est
alors corrigé dans le dépôt, depuis l'issue.

Vous n'avez pas à donner votre nom civil. Ce qui compte, c'est qu'une personne
se tienne derrière le compte : un profil qui montre un historique de travail
humain, ici ou ailleurs. Des issues ouvertes, des relectures, des discussions,
des contributions qui se lisent comme les vôtres. Un compte sans trace publique
ne peut pas porter un changement. Un compte piloté comme une campagne
automatisée non plus.

Ce qui s'est passé, pour que la règle se lise avec sa raison. Le 2026-11-16, un
compte créé en 2016, presque inactif jusque-là, sans nom ni bio, a ouvert trois
pull requests ici en trois minutes (#985, #986, #987). Le soir même, il en
avait ouvert plus de 180 sur GitHub, depuis plus de 40 forks : un agent IA
lancé en campagne. Ses commits ne portaient pas le trailer `Assisted-by:`
avant qu'on le lui demande. Le code a été lu, relu, et
il était correct. Les trois pull requests ont quand même été fermées, et le
travail est refait dans le dépôt depuis les issues. Lire un diff dit si le code
est correct. Cela ne dit pas qui en répondra plus tard, ni ce que le même compte
enverra ensuite (#988).

### Pas de campagne

Gardez quelques pull requests ouvertes à la fois par compte, pas plus. Une série
de changements se discute d'abord dans une issue : dites ce que vous comptez
envoyer, et attendez une réponse avant d'ouvrir la suivante. Plusieurs pull
requests ouvertes en quelques minutes, par un compte que personne ici ne
connaît, se lisent comme une campagne, quel que soit leur contenu.

### Un chemin sensible passe d'abord par une issue

Certains fichiers font tourner du code sur les runners de la CI ou sur la
machine d'un mainteneur, ou décident de ce que lancent les autres contrôles. Un
changement à cet endroit se lit comme de la configuration et agit comme du code.
Ces chemins demandent une issue, et une réponse dedans, avant la pull request.
La liste vit dans un seul fichier, [`tools/ci/sensitive-paths.txt`](tools/ci/sensitive-paths.txt) :
`.github/` (workflows, gabarits, `CODEOWNERS`, rulesets), les scripts shell et
Python sous `tools/`, le code qui juge une pull request (`tools/ci/`,
`tools/review/`, `tools/testplan/`), `go.mod`, `go.sum`, les dépendances Python
verrouillées par hash sous `tools/`, `pyproject.toml`, `uv.lock`, `mise.toml`,
`.pre-commit-config.yaml`, le `Dockerfile`, les configurations des linters et
des scanners, et `tools/falsify/specs/` (une spec falsify est du code, pour la
raison donnée dans [Falsifier une garde](#falsifier-une-garde)).

Dites dans l'issue ce que vous voulez changer et pourquoi. Le contrôle
« Sensitive paths » reste rouge sur une pull request venue de l'extérieur qui
touche l'un de ces chemins, tant que le mainteneur n'a pas lu le changement,
approuvé la pull request par une revue, puis relancé le contrôle rouge, qui lit
alors l'approbation. L'approbation vaut pour le commit sur lequel elle a été
donnée : un push après elle remet le contrôle au rouge, jusqu'à ce que la
nouvelle tête soit approuvée à son tour et le contrôle rouge relancé. Le
contrôle est requis : ce rouge empêche la fusion. Le mainteneur lit une telle
pull request avec `tools/review/external-pr.sh` et lance ses tests, s'il les
lance, dans un conteneur jetable avec `tools/review/sandbox.sh` (voir
[`tools/review/README.md`](tools/review/README.md)), jamais sur un poste de
travail.

## Contributions assistées par IA

**Elles sont bienvenues, sous une condition, et la condition ne porte pas sur
l'IA.**

Ce projet se trouve être exceptionnellement bien équipé pour cette question. La
plupart des mainteneurs qui rejettent les pull requests générées par IA rejettent
une asymétrie : le contributeur passe une minute, le relecteur passe une heure à
prouver que le changement est faux. curl l'énonce comme un principe — *une
contribution devrait valoir au projet plus que le temps qu'il faut pour la
relire.*

Ici, la charge de la preuve est déjà de la machinerie. Un changement n'est pas
jugé à la façon dont il a été écrit ; il est jugé à ce que le client officiel le
pilote ou non. Ce contrôle coûte une commande au relecteur et il n'est pas
discutable :

```bash
mise run check          # gofmt, vet, lint, go test -race
mise run conformance    # scw, octl, exo, Terraform, OpenTofu, et la sonde
mise run drift:check    # la surface upstream correspond encore à la baseline
```

La règle est donc la même pour tout le monde : **apportez la preuve, pas
l'intention.** Une pull request qui dit « ceci émule DeleteSnapshot » et ne montre
rien est refusée, qu'elle ait été écrite par un humain ou un modèle. Une dont la
suite de conformance passe vaut d'être relue quoi qu'il l'ait produite.

Trois choses en découlent, et elles ne sont pas négociables.

### 1. Le déclarer

Utilisez le trailer git `Assisted-by:`, en nommant l'outil et la version du
modèle. C'est la convention vers laquelle le noyau Linux, Fedora, Mesa et QGIS ont
convergé, et elle existe pour qu'un futur lecteur de `git log` sache quoi
re-vérifier.

```text
feat(outscale): emulate Nic, LinkNic and UnlinkNic

Assisted-by: Claude Code (claude-opus-5)
```

Déclarez quand un modèle a écrit une part substantielle du changement. Ne vous
embêtez pas pour l'orthographe, le formatage, ou une complétion qui vous a
épargné trois frappes.

**Le trailer est exigé, pas demandé.** Une pull request dont un modèle a écrit
une part substantielle, et dont les commits ne le portent pas, est fermée. Cette
page le demandait seulement, et les trois pull requests du 2026-11-16 décrites
plus haut sont arrivées sans lui.

`Co-Authored-By` pour un outil reste interdit, et `Signed-off-by` venant d'autre
chose qu'une personne aussi. Ces trailers affirment une paternité et certifient
une origine ; un modèle ne peut faire ni l'un ni l'autre. L'humain qui soumet est
l'auteur, et prend la responsabilité de chaque ligne — licence comprise.

### 2. Le lancer avant de l'envoyer

Pas « les tests devraient passer » — **lancez-les**. Précisément :

- `mise run conformance`, ou au minimum la suite du provider que vous avez touché.
  Un test unitaire ne prouve jamais une forme de réponse ; seul le vrai client le
  fait. C'est la règle qui attrape le mode de défaillance le plus fréquent ici
  pour les changements assistés par IA : un nom de champ plausible que l'API du
  provider ne définit pas.
- Avec `--contracts contracts`, pour que chaque réponse soit validée contre le
  document OpenAPI du provider lui-même. Un champ inventé par un modèle fait
  échouer le run.
- Vérifiez `/_feint/conformance | jq .unread_request_fields`. Un champ déclaré sur
  une struct de requête et jamais lu est invisible pour ce rapport — c'est le seul
  angle mort, et c'est ainsi qu'une Vm a rapporté un succès sans aller nulle part.

### 3. N'envoyez pas ce que vous n'avez pas lu

La défaillance spécifique dont ce projet souffrirait n'est pas du mauvais code ;
c'est du **code assuré qui émule une API que personne n'a vérifiée**. Un modèle
produira volontiers un handler pour `CreateSnapshot` avec une forme de réponse qui
se lit parfaitement et ne correspond à rien en amont, et il écrira un test
affirmant sa propre invention — ce qui est exactement le défaut que
[`docs/limits.md`](docs/limits.md) enregistre comme ayant déjà été livré une fois,
avec un `private_ip` inventé que sa propre suite relisait.

Donc : si vous ne pouvez pas dire d'où vient un nom de champ — leur SDK, leur
document OpenAPI, ou un run du vrai client —, ne l'envoyez pas. « Le modèle l'a
produit » n'est pas une source. C'est tout : **en cas de doute, lisez le SDK, pas
la documentation, et n'inventez jamais un format.**

### Ce qui est refusé d'emblée

- Une pull request dont aucune personne identifiable ne répond, ou qui fait
  partie d'une campagne. Voir [Qui répond d'une pull request](#qui-répond-dune-pull-request).
- Une pull request dont un modèle a écrit une part substantielle sans qu'un
  trailer `Assisted-by:` le dise.
- Une pull request dont la suite de conformance n'a pas été lancée, et qui le dit
  ou le montre.
- Un lot de changements sur plusieurs packs sans une seule affirmation relisible.
- Un changement qui ajoute une route sans `Route.Operation` au nom upstream, ou
  sans mettre à jour `coverage/`. Les hooks pre-commit attrapent les deux ; une
  pull request qui les a fait échouer et a été envoyée quand même est un signal
  sur le reste.
- Tout ce dont la justification annoncée est d'améliorer une métrique —
  pourcentage de couverture, nombre de routes — plutôt que de débloquer un client.

Rien de tout cela ne vise l'IA. Le trailer demande de dire qu'un modèle a servi,
pas de s'en passer. Le reste est ce que le projet demandait déjà à tout le
monde ; l'écrire est ce qui rend la réponse identique pour les deux.

## Code

Go, bibliothèque standard d'abord. Une nouvelle dépendance est une décision à
justifier dans la pull request, pas un détail. Au-delà : des erreurs explicites,
pas d'abstraction prématurée, les tests à côté du code, et un commentaire qui dit
pourquoi plutôt que quoi.

## Sécurité

N'ouvrez pas d'issue publique pour une vulnérabilité. Signalez-la en privé via le
signalement privé de vulnérabilité de GitHub.

Souvenez-vous de ce qu'est Feint : un service qui n'authentifie personne et
accorde tout. Tout changement qui facilite son exposition sur un vrai réseau
mérite un examen.
