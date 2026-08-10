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

Elle installe deux types de hooks, et le second se rate facilement : `pre-commit`
et `commit-msg`. La configuration déclare les deux via
`default_install_hook_types`, donc la commande seule suffit.

### Si vous contribuez depuis un fork

Rien ne tournera sur votre pull request tant qu'un mainteneur ne l'aura pas
approuvée. GitHub garde chaque workflow en `action_required` pour les
contributions venant de forks, ce qui est le bon défaut — c'est ce qui empêche un
fork de modifier un workflow pour lire les secrets de ce dépôt — mais cela veut
dire que `gh pr checks` répond « no checks reported » et que vous n'avez aucun
retour, pas même un échec.

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

Le tout, en cinq points :

1. Lire le source du SDK upstream pour les formes exactes, pas la doc web.
2. Tracer le vrai client (`scw -D <commande>`) et émuler la séquence **entière** :
   une création est rarement un seul appel.
3. Déclarer l'opération upstream sur chaque route ; déclarer ce que vous ne servez
   pas dans `Declined()`, avec une raison.
4. Les tests : aller-retour de cycle de vie, pagination, cloisonnement, un par
   forme d'erreur. Fuzzer tout nouveau décodeur de requête.

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

**Les milestones** sont les six vagues de la séquence de la roadmap — un ordre,
pas un calendrier, donc elles ne portent pas de date d'échéance.

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
mise run conformance    # scw, oapi-cli, exo, Terraform, OpenTofu, et la sonde
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

- Une pull request dont la suite de conformance n'a pas été lancée, et qui le dit
  ou le montre.
- Un lot de changements sur plusieurs packs sans une seule affirmation relisible.
- Un changement qui ajoute une route sans `Route.Operation` au nom upstream, ou
  sans mettre à jour `coverage/`. Les hooks pre-commit attrapent les deux ; une
  pull request qui les a fait échouer et a été envoyée quand même est un signal
  sur le reste.
- Tout ce dont la justification annoncée est d'améliorer une métrique —
  pourcentage de couverture, nombre de routes — plutôt que de débloquer un client.

Rien de tout cela ne vise l'IA. C'est ce que le projet demandait déjà à tout le
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
