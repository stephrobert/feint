<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/brand/feint-lockup-dark.svg">
  <img src="docs/assets/brand/feint-lockup-light.svg" alt="Feint" width="230">
</picture>

# Feint

*Un émulateur local des clouds européens — Scaleway, Outscale, Exoscale.*

**Un binaire. Un port. Pas de compte, pas de facture.**

[![Conformance](https://github.com/stephrobert/feint/actions/workflows/conformance.yml/badge.svg)](https://github.com/stephrobert/feint/actions/workflows/conformance.yml)
[![Drift](https://github.com/stephrobert/feint/actions/workflows/drift.yml/badge.svg)](https://github.com/stephrobert/feint/actions/workflows/drift.yml)
[![OpenSSF Scorecard](https://img.shields.io/ossf-scorecard/github.com/stephrobert/feint?label=OpenSSF%20Scorecard)](https://securityscorecards.dev/viewer/?uri=github.com/stephrobert/feint)
[![Zéro dépendance](https://img.shields.io/badge/dependencies-0-success)](./go.mod)
[![Licence Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](./LICENSE)

**Autre langue :** [English](./README.md)

[Installer](#installer) • [S'en servir](#sen-servir) • [Ce qui est prouvé](#état) •
[Commandes](#commandes) • [Ce qu'il ne fait pas](docs/limits.md) •
[Contribuer](CONTRIBUTING.md)

> [!WARNING]
> **En développement actif, version 0.x, et pas exempt de bugs.** Deux audits
> adverses par pack, un par provider, ont trouvé des défauts sur tous les
> chemins que la suite de conformance ne parcourt pas — y compris dans les
> correctifs du tour précédent. Ce qu'un vrai client est prouvé piloter est
> compté dans *[État](#état)* et dans `/_feint/conformance` ; ce qui est refusé,
> et pourquoi, est dans [docs/routes.md](docs/routes.md). Tout le reste n'est pas
> prouvé tant que ce n'est pas mesuré. Ne pointez rien qui compte vers lui, et ne
> lisez pas une commande qui passe comme une garantie : signalez ce qui casse,
> c'est ce qui fait bouger les chiffres.

> [!NOTE]
> L'anglais est la source. Cette page est une traduction, et elle peut avoir
> pris du retard : en cas de désaccord entre les deux, [la version
> anglaise](README.md) fait foi. Les blocs générés par `feint docs` — la version
> publiée, le tableau des prérequis, les commandes d'installation, les versions
> des clients, les tableaux de couverture — restent en anglais dans les deux
> pages, parce qu'ils sont rendus depuis le code plutôt qu'écrits à la main.

## Démarrage rapide

```bash
feint serve
#  feint dev listening on 127.0.0.1:4599
#    scaleway  57 routes
#    outscale  72 routes
#    exoscale  46 routes
#    machines  none
```

C'est un seul point d'entrée pour trois clouds. Ils ne se marchent pas dessus
dans l'espace des URL — Scaleway sert `/<produit>/v<N>/…`, Outscale
`POST /api/v1/<Action>`, Exoscale `/v2/<ressource>` — donc un seul
`http.ServeMux` les héberge tous les trois, et le serveur refuse de démarrer si
deux packs revendiquent la même route.

![Démarrage de Feint, le CLI Scaleway officiel pointé dessus, et un terraform apply exécuté contre lui](docs/assets/quickstart.gif)

Chaque commande de cet enregistrement a réellement tourné : l'émulateur démarre,
le CLI officiel `scw` crée un serveur, le provider Terraform officiel applique
une configuration via `api_url`, et l'émulateur s'arrête. Il est produit depuis
[`tools/demo/quickstart.tape`](tools/demo/quickstart.tape) par `mise run demo`,
donc une commande qui casse casse la démonstration. Un enregistrement que
personne ne peut rejouer est une affirmation, et tout l'argument ici est qu'une
affirmation n'est pas une preuve.

Une seconde bande couvre ce qu'un mock ne sait pas faire :
[`tools/demo/network.tape`](tools/demo/network.tape) pilote le CLI Outscale
officiel pour créer un Net, un Subnet et une Vm, puis demande **au runtime plutôt
qu'à l'émulateur** ce qui existe — `incus list` montre un conteneur portant
l'adresse exacte que l'API a publiée, sur ce subnet, et ne portant plus rien une
fois l'émulateur arrêté. Il faut Incus avec OVN ; `mise run demo:network`
l'enregistre.

---

## Sommaire

- [Démarrage rapide](#démarrage-rapide)
- [Installer](#installer)
- [S'en servir](#sen-servir)
- [La page](#la-page)
- [De vraies machines, à la demande](#de-vraies-machines-à-la-demande)
- [État](#état)
- [Commandes](#commandes)
- [Contexte](#contexte)
- [Contribuer](#contribuer)
- [Licence](#licence)

---

## Installer

### Prérequis

**Aucun, pour ce que la plupart des gens viennent chercher.** Feint est un seul
binaire statique sans dépendance externe : il émule les trois plans de contrôle,
garde son état en mémoire, et n'a besoin d'aucun démon, d'aucun runtime de
conteneurs et d'aucun compte. Si vous voulez pointer `scw`, `oapi-cli`, `exo` ou
Terraform vers un émulateur, arrêtez de lire ici et allez à
[Installer](#installer).

Deux choses ne servent qu'à ce qu'elles permettent : **Go 1.26** pour construire
depuis les sources — un binaire publié n'a besoin de rien — et
[**Incus**](https://linuxcontainers.org/incus/), 6.0.4 au minimum et 7.2
recommandé, pour `--vm` : les serveurs allumés deviennent alors de vrais
conteneurs ou de vraies machines KVM dans lesquelles on peut entrer en `ssh`.
**OVN** en plus (`ovn-central`, `ovn-host`, Open vSwitch) donne
`--vm incus-ovn` : des subnets réellement séparés, donc deux VPC qui ne peuvent
pas s'atteindre.

6.0.4 est un plancher et non une préférence : en dessous, le runtime refuse les
ACL sur une NIC, et l'échec ressemble à un bug de Feint plutôt qu'à une
fonctionnalité manquante. Ubuntu 24.04 livre 6.0.0 et n'ira pas au-delà, donc le
canal stable Zabbly est la voie pratique vers une version supportée.
`feint doctor` vérifie tout cela contre ce même 6.0.4, et dit quoi installer,
ce qui est l'intérêt de l'avoir. Le tableau exact des versions est dans [la
version anglaise](README.md#prerequisites), rendu depuis le code.

### Un binaire publié

Les commandes d'installation, avec la vérification de signature et de somme de
contrôle, sont dans [la version anglaise](README.md#a-released-binary) : elles
sont générées depuis le code et portent la version publiée du jour, donc les
recopier ici les ferait mentir dès la release suivante.

En résumé de ce qu'elles font, parce que cela vaut d'être compris avant d'être
exécuté : chaque fichier est téléchargé sur disque et vérifié **avant** que quoi
que ce soit ne s'exécute. `cosign` établit d'abord **qui** a produit la liste des
sommes, puis `sha256sum` vérifie les octets contre cette liste. Une seule
signature couvre tous les artefacts, puisque toutes les empreintes sont dans ce
fichier. La version est nommée plutôt que récupérée en `latest` : une référence
mutable télécharge ce qui est le plus récent, c'est-à-dire un binaire que
personne ne peut nommer après coup.

Avec `gh`, la vérification porte sur la **provenance de build** : elle prouve
quel workflow et quel commit ont produit le binaire, là où la signature prouve
qui a publié la liste.

`docs/install.md` reproduit le même bloc depuis la même source, donc les deux
pages ne peuvent pas diverger.

Ou depuis le proxy de modules, qui exige une chaîne Go, construit au lieu de
télécharger, et ne vérifie donc rien de ce qui précède :

```bash
go install github.com/stephrobert/feint/cmd/feint@latest
```

### Depuis les sources

La version de Go des prérequis, et rien d'autre. Le module n'a **aucune
dépendance externe** — un hook pre-commit l'y tient, et `go.mod` fait trois
lignes.

```bash
git clone https://github.com/stephrobert/feint
cd feint
go build -o feint ./cmd/feint
./feint version
```

### Avec mise, pour développer

```bash
mise trust && mise install     # Go, Python, uv et les linters, épinglés
mise run serve                 # l'émulateur sur 127.0.0.1:4599
mise tasks                     # tout le reste
```

---

## S'en servir

Démarrez l'émulateur une fois. Tout ce qui suit parle au même processus.

```bash
feint start          # se détache, attend qu'il réponde, dit où est le journal
feint status         # ce qu'il monte, et combien un client en a réellement piloté
curl -s localhost:4599/_feint/health | jq -c '{status, machines, capabilities}'
```

`capabilities` est ce que le mode en cours sait réellement prouver. Sans runtime
de machines tout y est faux et l'émulateur est un plan de contrôle ; la section
[De vraies machines](#de-vraies-machines-à-la-demande) dit ce que chaque mode
change.

`start` se met en arrière-plan tout seul — pas de `&`, pas de Docker. Aucun des
émulateurs comparables ne le peut : une JVM et un processus CPython ne se
démonisent pas proprement, donc LocalStack, floci et ministack délèguent tous le
détachement à `docker run -d`. Un binaire Go statique n'en a pas besoin.

Ensuite, pointez un client dessus sans rien recopier à la main :

```bash
eval "$(feint env scaleway)"
scw instance server list zone=fr-par-1
```

`env` n'écrit que des lignes `export` sur la sortie standard, donc `eval` est
sûr ; chaque indication et chaque mise en garde vont sur la sortie d'erreur.
`feint env scaleway --unset` est le chemin de retour vers le vrai cloud dans le
même shell.

Les credentials qu'il imprime sont volontairement faux et volontairement
publics. L'émulateur ne les vérifie jamais ; les clients officiels refusent de
signer une requête dont les credentials ne sont pas *bien formés*, ce qui est la
seule raison de leur existence. Ils vivent dans
`tools/conformance/<provider>/fake-credentials.env`, et `env` lit les mêmes
valeurs, donc le CLI et les suites de conformance ne peuvent pas diverger.

### Scaleway — le CLI `scw`

`scw` lit son endpoint dans `SCW_API_URL`, donc rien d'autre n'est nécessaire.

```bash
eval "$(feint env scaleway)"
scw instance server create name=demo type=DEV1-S image=ubuntu_jammy zone=fr-par-1
scw instance server list zone=fr-par-1
```

Terraform y accède par l'attribut `api_url` du provider. Une configuration qui
fonctionne est dans
[`tools/conformance/scaleway/terraform/`](tools/conformance/scaleway/terraform/),
et `terraform` comme `tofu` sont exécutés contre elle en CI.

### Outscale — `oapi-cli`

`oapi-cli` n'a pas de drapeau `--endpoint` : il se configure par un profil JSON.

**Un piège qui coûte une heure**, mesuré ici : `oapi-cli` lit la clé `region`,
jamais `region_name`. Cette dernière est la clé d'`osc-cli`, le client Python —
un autre outil qui lit le même fichier. Un profil écrit pour l'un fait répondre
`InvalidParameterValue 4120` à tous les appels authentifiés de l'autre, pendant
que les appels publics continuent de passer et masquent la cause.

Le détail et la configuration exacte sont dans
[`tools/conformance/outscale/`](tools/conformance/outscale/).

### Exoscale — le CLI `exo`

`exo` n'a ni drapeau ni variable d'endpoint au sens habituel : il se redirige par
`EXOSCALE_API_ENDPOINT`, **et cette valeur doit porter le suffixe `/v2`**, que le
CLI concatène avec la route au lieu de le remplacer.

Le provider Terraform d'Exoscale est **refusé** par l'émulateur, et ce n'est pas
un choix : il construit deux clients internes, l'un honore l'endpoint et l'autre
part vers le vrai cloud, donc un `apply` se retrouverait à cheval entre
l'émulateur et un compte payant. Le raisonnement complet est dans
[docs/limits.md](docs/limits.md).

### Demander à l'émulateur ce qu'il fait

```bash
curl -s localhost:4599/_feint/health | jq .
curl -s localhost:4599/_feint/routes | jq .
curl -s localhost:4599/_feint/conformance | jq .
```

---

## La page

Le binaire sert une page sur lui-même, à `/_feint/ui`, ouverte par `feint ui`.

Elle montre les routes montées, celles qu'un vrai client a réellement pilotées et
celles qu'une sonde seule a atteintes — **sans jamais les additionner** —, l'écart
avec l'API amont de chaque provider, tout ce que la session a créé, et un journal
des appels en direct. Chaque agrégat s'ouvre sur ce dont il est fait.

Elle est **en lecture seule** et servie **sur la boucle locale uniquement** : hors
loopback elle n'est pas cachée, elle n'existe pas. Et **aucune authentification,
jamais** — un secret dans le navigateur de l'opérateur est hérité par la page
hostile, ce qui est la définition du CSRF. Le contrôle qui fonctionne est le refus
d'origine, et il est déjà mesuré.

---

## De vraies machines, à la demande

Par défaut, Feint est un plan de contrôle : il répond comme le cloud et ne démarre
rien. `--vm` change cela.

```bash
FEINT_VM=incus     mise run serve   # conteneur système, noyau partagé
FEINT_VM=incus-vm  mise run serve   # machine KVM, noyau propre
FEINT_VM=incus-ovn mise run serve   # réseaux OVN : les subnets sont isolés
```

### Les modes ne sont pas équivalents, et une différence compte

Un seul contrôle de toute la suite réseau change de verdict selon le mode, et
c'est celui qui porte l'argument du produit : **l'isolation entre deux VPC**.

Le mode pont ne peut pas la livrer — deux bridges gérés d'un même hôte sont
routés directement entre eux, le runtime le documente. OVN la livre par
construction, un réseau logique ayant son propre routeur. Tout le reste — les
machines, les adresses, le pare-feu — tient dans les deux modes.

Ce qu'un mode sait faire est **déclaré**, pas déduit d'un nom : voir
`capabilities` sur `/_feint/health`. Et une capacité non déclarée vaut absente,
de sorte qu'un contrôle se saute plutôt que d'affirmer ce que personne n'a promis.

Corollaire pour la documentation : ne jamais écrire « les subnets sont isolés »
sans nommer le mode.

---

## État

Le nombre qui compte est celui des routes qu'un **vrai client** est prouvé
piloter. Il est publié par `/_feint/conformance`, affiché par `feint status`, et
la suite `mise run conformance` le recalcule à chaque exécution.

Les tableaux de couverture par provider et par produit sont dans [la version
anglaise](README.md#status) : ils sont générés depuis les artefacts versionnés,
donc les recopier ici serait en faire une seconde source qui se périmerait.

Ce qui est refusé, et pourquoi, est dans [docs/routes.md](docs/routes.md). Ce qui
n'est délibérément pas émulé est dans [docs/limits.md](docs/limits.md).

---

## Commandes

La liste complète est donnée par `feint --help`, et elle fait autorité sur cette
page. Les verbes principaux :

| commande | ce qu'elle fait |
| --- | --- |
| `feint serve` | les trois clouds émulés sur un port, au premier plan |
| `feint start` / `stop` / `restart` | le même, détaché, avec un cycle de vie surveillé |
| `feint status` | ce qui tourne, ce qu'il monte, ce qu'un client a piloté |
| `feint ui` | ouvre la page que le binaire sert sur lui-même |
| `feint doctor` | diagnostique l'hôte : le port, le runtime, les clients |
| `feint env <provider>` | l'environnement dont un vrai client de ce provider a besoin |
| `feint proxy` | enregistre ce qu'un vrai client et un vrai cloud se disent |
| `feint shapes` | ce qu'un vrai cloud renvoie, et ce que l'émulateur en omet |
| `feint coverage` | la surface amont servie, déclinée ou non triée |
| `feint snapshot` | sauvegarde et recharge l'état |
| `feint clean` | retire les machines et réseaux que l'émulateur a créés |

Les codes de sortie sont stables, parce que la CI en dépend : **0** succès,
**1** erreur, **2** dérive détectée.

---

## Contexte

AWS a LocalStack. Azure a Azurite. Les clouds européens n'avaient rien, et leurs
utilisateurs testent contre un compte payant ou pas du tout.

Feint émule leurs API pour que les SDK, les CLI et Terraform tournent contre
votre poste. **Scaleway, Outscale et Exoscale sont les trois premiers** —
l'architecture existe pour qu'un quatrième ne change rien en dehors de son propre
pack, et le scan qui les tient honnêtes lit le SDK ou la description d'API que ce
provider publie.

Le nom est le mouvement d'escrime : *feinte*, de l'ancien français *feindre* — un
geste fait pour ressembler au vrai, afin que l'adversaire s'engage. C'est tout le
test ici. Le client officiel s'engage, et ne voit pas la différence.

### Pourquoi pas un serveur de mock

Un mock rend ce qu'on lui a dit de rendre. Il prouve que votre code appelle ce
que vous croyez qu'il appelle, jamais que le cloud répondrait ainsi. Et il ne
tient aucun état : un `terraform apply` suivi d'un `plan` vide demande que la
ressource créée se relise à l'identique, ce qu'un mock ne fait pas sans qu'on
écrive la réponse à la main pour chaque étape.

### Pourquoi pas de l'émulation écrite à la main

Parce que la surface bouge plus vite qu'une équipe. Scaleway a ajouté 453
méthodes de SDK et en a retiré 26 en douze mois : personne ne suit cela de tête.

C'est pourquoi ce projet **mesure** l'API au lieu de la suivre. Un scan lit le
SDK officiel du provider, une baseline versionnée fait échouer la CI dès qu'une
opération apparaît sans avoir été triée, et la conformance rejoue les vrais
clients. Depuis peu, `feint proxy` enregistre en plus ce qu'un vrai cloud répond,
ce qui a révélé des champs que l'émulateur omettait et qu'aucun autre contrôle ne
pouvait voir.

---

## Contribuer

Lisez d'abord [CONTRIBUTING.md](CONTRIBUTING.md) — en anglais, comme le code, les
commentaires et les issues. Il porte la règle unique de ce dépôt : **un changement
de surface émulée n'est pas terminé tant qu'un vrai client ne l'a pas exercé**,
parce qu'un test unitaire affirme ce qu'on croit qu'une API fait, et que seul le
client officiel affirme ce qu'elle est.

```bash
mise install           # Go, Python, uv et les linters, épinglés
pre-commit install     # les gardes locaux ; pas optionnel
mise run check         # gofmt, vet, golangci-lint, go test -race
mise run conformance   # tous les vrais clients contre lui
```

- Architecture : [docs/architecture.md](docs/architecture.md)
- Ce qui n'est volontairement pas émulé : [docs/limits.md](docs/limits.md)
- Où cela va : [docs/roadmap.md](docs/roadmap.md)
- Signaler une vulnérabilité : [SECURITY.md](SECURITY.md)

---

## Licence

[Apache 2.0](LICENSE).

Feint n'est **ni affilié à, ni approuvé, ni sponsorisé, ni certifié par**
Scaleway, Outscale ou Exoscale. Leurs noms sont employés pour désigner les API
émulées, ce qui est un usage descriptif.
