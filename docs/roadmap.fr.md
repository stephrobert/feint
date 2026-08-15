# Feuille de route

**Autre langue :** [English](./roadmap.md)

> [!NOTE]
> C'est [roadmap.md](./roadmap.md) que lisent les issues, les jalons et les
> outils : c'est donc lui la source. Cette page en est la traduction, mise à
> jour avec lui. Un chiffre ne se corrige jamais ici d'abord.

Ce que ce projet compte faire ensuite, pourquoi, et comment chaque élément sera
reconnu comme fait. L'ordre est celui de ce qui débloque un utilisateur, pas de
ce qui est intéressant à construire.

Ce qui est *mesuré* plutôt que planifié vit dans [limits.md](limits.md) ; la
façon dont les pièces s'assemblent, dans [architecture.md](architecture.md).

## Comment lire cette page

Chaque élément énonce sa **preuve** : la chose qui sera vraie quand il sera
fait, exprimée de façon qu'une machine puisse la vérifier. « Terraform
applique » est une preuve. « Le code le permet » n'en est pas une : toute la
thèse de ce projet est qu'un test unitaire ne prouve rien d'une forme de
réponse, et une feuille de route écrite en intentions serait la même erreur
sous une autre forme.

Les pourcentages de la surface amont figurent dans le README et sont générés
depuis les artefacts de couverture versionnés. Ils sont volontairement absents
d'ici : une feuille de route qui suit un pourcentage optimise le pourcentage.
Même règle pour les comptes : quand un élément dépend d'un nombre, la preuve
renvoie aux tableaux générés au lieu de figer un chiffre qui pourrira. Cette
page a déjà payé cette règle une fois : ses compagnons par provider ont figé
leurs comptes le 30 juillet 2026 et chacun était faux quinze jours plus tard
(#127) ; ce sont des archives désormais, sous [history/](history/).

---

## Maintenant : ce qui est en construction

### L'image conteneur, control plane seul, livrée avec la release

**Livrée par #150**, et publiée pour la première fois avec la 0.8.0.

C'était une décision plus qu'une fonctionnalité, donc la voici par écrit :
**l'image exécute `feint serve` avec `--vm off`, et n'émule rien d'autre que le
plan de contrôle.** La question qui la retenait (où atterriraient des machines
démarrées dans un conteneur) est résolue comme le reste du projet la résout
déjà : le mode par défaut n'exige aucun runtime, la suite de conformance tourne
sans lui en CI, et `serve` au premier plan est exactement ce qu'attend un point
d'entrée de conteneur. Qui a besoin de vraies machines exécute le binaire sur
un hôte avec Incus ; c'est le chemin documenté et il le reste.

Pourquoi elle est passée avant les canaux d'adoption : l'image est le format
dans lequel un émulateur se consomme, et chaque canal plus bas (testcontainers,
un fichier compose, un bloc `services:` de GitLab CI) l'attend. Ce que l'image
ne doit jamais devenir : le mode nominal. Le binaire statique qui se détache
tout seul est la seule chose qu'aucun émulateur comparable ne sait faire, et
mener avec Docker l'effacerait.

**Prouvée par :** `release.yml` publie une image multi-architecture sur
ghcr.io, et le job `image` de `conformance.yml` fait tourner la suite de
conformance Scaleway depuis l'hôte contre l'émulateur qui tourne dans cette
image, sur chaque pull request.

### Le scénario golden image sur Scaleway : les deux moitiés livrées

Le manque le plus coûteux de la surface servie n'a jamais été un pourcentage,
c'est un scénario : construire une image avec Packer ou un script `scw`,
attacher un volume avec un module Terraform ordinaire.

**La première moitié est livrée avec SW-2 (#7, mergée en #131).** Snapshots et
images sont des enregistrements du plan de contrôle : un client snapshotte un
volume, découpe une image depuis le snapshot, la liste à côté du catalogue
fixe, et l'ordre de suppression est imposé. L'attachement de volume est servi.
Ce qu'une image découpée ici ne peut pas faire, c'est démarrer (cet émulateur
garde des enregistrements, pas des contenus de disque) et il le dit au
démarrage au lieu de substituer une distribution ([limits.md](limits.md) porte
le refus, #115 la décision). La colonne « untriaged » d'`instance` dans les
tableaux générés lit zéro, par décision : ce qui n'a pas été servi (les
placement groups entre autres) est décliné avec sa raison dans le pack.

**La seconde moitié est livrée avec SW-3 (#8, mergée en #138).** `block/v1` et
le volume racine `sbs_volume` ont refermé le piège mesuré de
[limits.md](limits.md), où le provider relisait un volume à travers une API
qu'aucun pack ne servait et où l'apply mourait sur un 404. Cette page ouvre
désormais sa section volume racine sur `sbs_volume` qui fonctionne, ce qui était
la définition de la fin de cet élément.

Un détail mérite d'être gardé, parce qu'il a été trouvé par le client et non en
lisant le SDK : `scw` 2.56.3 appelle `/block/v1alpha1` là où le provider
Terraform appelle `/block/v1`. Les deux sont servis, par les mêmes handlers.

**Prouvée par :** `terraform apply` avec un `scaleway_block_volume` et un volume
racine `sbs_volume`, un second plan vide et un destroy propre, dans la suite de
conformance Scaleway.

### Exoscale était étiqueté preview, et l'étiquette est tombée à EXO-2

**Réglé.** Exoscale est *starter*, aux côtés d'Outscale, depuis EXO-2.

L'étiquette avait été posée délibérément plutôt que laissée glisser : le pack
est sorti marqué **preview** parce que le CLI officiel `exo` le pilotait de
bout en bout alors qu'un utilisateur ne pouvait toujours pas y faire tourner
une charge réaliste. L'état intermédiaire, servi mais pas honnêtement
utilisable, est celui qui abîme la crédibilité, c'est-à-dire le capital de ce
projet.

Cette prémisse n'est plus vraie. EXO-2 sert le cycle de vie des instances, les
groupes de sécurité et leurs règles, les groupes d'anti-affinité, les IP
élastiques et leur attachement, et la suite `exo` pilote chacun d'eux : stop,
start, reboot, scale, resize, une suppression refusée tant que protégée, une
adresse publiée sur une instance puis retirée.

**La condition de sortie elle-même était fausse, et cela vaut d'être
consigné.** Elle disait *jusqu'à ce que le provider Terraform soit prouvé
contre l'émulateur*, ce qui supposait que le provider Terraform Exoscale
puisse être pointé vers un émulateur. La mesure l'a réfuté : le provider
honore `EXOSCALE_API_ENDPOINT` pour son client egoscale v3 et construit un
client v2 sans aucune option d'endpoint, si bien qu'un apply se partage entre
l'émulateur et un compte payant. `ClientOptWithAPIEndpoint` existe dans
egoscale et n'est jamais appelé ; trois sites construisent un client v2 sans
lui. Déposé en amont sous
[exoscale/terraform-provider-exoscale#573][exo-573], avec le mécanisme et une
reproduction ; le raisonnement et un build corrigé sont dans
[limits.md](limits.md#the-exoscale-terraform-provider-is-refused-and-why).

Une condition que personne ici ne peut atteindre n'est pas une condition,
c'est un otage. La garder aurait fait dépendre la maturité publiée de ce
projet du tracker de quelqu'un d'autre, pour une durée que personne ne
contrôle, alors que ce dont l'étiquette prévenait était déjà corrigé.

**Ce qui sépare encore Exoscale d'« utilisable » est dit dans les tableaux de
couverture plutôt qu'en un mot :** sa colonne « untriaged » est la plus grande
des trois, elle est générée, et elle ne peut pas flatter. Ce paragraphe
recopiait autrefois les trois nombres ; ils ont pourri moins vite que ceux des
documents archivés uniquement parce qu'ils étaient plus jeunes.

[exo-573]: https://github.com/exoscale/terraform-provider-exoscale/issues/573

---

## Les trois couches IaaS, une seule séquence

La couche IaaS de chaque provider a été mesurée et découpée en lots dans un
instantané daté du 30 juillet 2026 :
[Scaleway](history/roadmap-2026-07-30-scaleway-iaas.md),
[Outscale](history/roadmap-2026-07-30-outscale-iaas.md),
[Exoscale](history/roadmap-2026-07-30-exoscale-iaas.md). Ces documents sont
désormais des **archives** : le raisonnement qui a ordonné les lots est
l'archive, les chiffres sont ceux de ce jour-là, et chacun porte une bannière
qui le dit (#127). Ce qui est actuel vit là où cela se régénère (les tableaux
du README, [routes.md](routes.md)) ; ce qui reste ouvert vit dans les jalons
de vague et leurs issues. Cette section ordonne les lots des trois en une
seule séquence et nomme le travail transversal.

### Ce que « couverture acceptable » veut dire, et ne veut pas dire

Pas un pourcentage. Un pourcentage de la surface amont récompense cent
lectures faciles plutôt que la seule écriture sur laquelle meurt le premier
`terraform apply` d'un utilisateur, et les tableaux générés du README portent
déjà les pourcentages pour qui les veut. La couverture est acceptable pour un
provider quand trois phrases sont vraies, chacune vérifiable par une machine :

1. **Le CLI officiel déroule le cycle de vie machine de bout en bout** :
   create, list, get, stop, start, delete, avec une clé SSH enregistrée et une
   adresse publiée, contre l'émulateur, dans la suite de conformance.
2. **Une configuration Terraform réaliste applique, re-planifie vide et
   détruit proprement**, contrats actifs. « Réaliste » ne se choisit pas ici :
   pour Outscale c'est le `examples/net_vm` du provider lui-même ; pour
   Scaleway le module golden image ci-dessus ; pour Exoscale la pile
   d'instance ordinaire, prouvée par `exo`, puisque son provider Terraform ne
   peut pas être pointé ici ([#573][exo-573]).
3. **La colonne « untriaged » lit zéro pour les produits que la feuille de
   route de ce provider déclare dans le périmètre** : zéro par décision, servi
   ou décliné avec une raison, jamais par élargissement du dénominateur.

La troisième phrase est ce qui garde les deux premières honnêtes dans la
durée : un scénario prouvé une fois ne reste prouvé que parce que le gate
échoue quand la surface bouge dessous.

### Le travail transversal aux trois packs

Nommé ici parce que chaque élément semble local dans un lot et ne l'est pas.
Deux ont été livrés et restent listés parce qu'ils nomment désormais le
mécanisme à imiter ; trois sont des règles permanentes.

- **`Declined()` porte une raison** : livré avec X-1, et la doctrine est
  depuis descendue d'un niveau : #122 donne à un pack `DeclinedFields()`, un
  champ d'une réponse observée qu'il ne sert sciemment pas, avec la même garde
  anti-placeholder sur la raison. « Non servi » et « non trié » sont des
  réponses différentes à toutes les granularités.
- **La question de la preuve Terraform est réglée par provider, pas
  globalement** : Scaleway et Outscale ont chacun leur fixture dans
  `tools/conformance/`, et celle d'Outscale pilote le `examples/net_vm` du
  provider plus sa chaîne de stockage. Celle d'Exoscale est *impossible
  plutôt que manquante*, mesuré et déposé en amont ([#573][exo-573]) ; sa
  preuve est le CLI officiel, et [limits.md](limits.md) explique pourquoi la
  preuve par provider corrigé ne compte volontairement pas.
- **Le contrat s'étend avec chaque produit, jamais après lui.** Un nouveau
  produit entre dans l'extraction `tools/contract/` dans le même changement
  que ses routes. Outscale et Exoscale rendent cet ordre obligatoire : leurs
  contrats `additionalProperties: false` refusent net les réponses d'un
  produit non extrait.
- **Tout nouveau chemin de cycle de vie prend le verrou par cible**
  (`machine.Binding.Serialise`, qui existe et est pris par les trois packs) et
  le prouve par un test de concurrence, comme le fait
  `TestConcurrentPowerOnStartsTheMachineOnce`. Rien ne le rappellera à
  personne ; seul le test le fait. (#134 propose le complément au niveau
  scénario : des invariants tenus sous un barrage délibéré, pas seulement
  chaque verrou sous sa propre course.)
- **Un adossement runtime n'arrive que comme capacité déclarée.** Stockage
  bloc, load balancers et gateways sortent d'abord en plan de contrôle ; un
  pilote qui gagne un adossement réel le déclare (`machine.Capabilities`), et
  une capacité non déclarée vaut absente.

### La séquence

Ordonnée par ce qui débloque un utilisateur, le même critère que le reste de
cette page. Les identifiants de lot sont ceux des issues et des jalons ; les
documents archivés expliquent comment chaque lot a été découpé.

1. **La vague de triage** : **faite.** Lot 1 de Scaleway, Outscale et
   Exoscale, portant le changement `(operation, reason)` (X-1). Elle a
   transformé trois colonnes « untriaged » illisibles en listes de travail et
   placé iam et marketplace sous le gate auquel ils échappaient. Sa preuve
   tient telle quelle : le gate rend 0 sur les baselines, et les colonnes des
   tableaux générés sont des listes de travail, pas des murs.
2. **Première preuve Terraform pour Outscale, et le cycle de vie machine pour
   Exoscale** : **faite.** OSC-2 a amené `terraform apply`, second plan vide
   et destroy propre en conformance ; EXO-2 a amené le cycle de vie sous `exo`
   et fait tomber l'étiquette *preview*, comme consigné plus haut.
3. **Le scénario golden image Scaleway** : **faite.** SW-2 (#7) mergée en #131,
   SW-3 (#8) en #138. Voir l'élément « Maintenant », qui est cette vague.
4. **Des réseaux qui routent** : **Outscale et Exoscale faits, un ouvert.**
   OSC-3 est mergée : le `examples/net_vm` du provider applique, re-planifie
   vide et détruit. EXO-3 (#9) est mergée en #161 : un réseau privé est une
   plage, et un attachement y prend un bail. SW-4 (#11, cycle de vie IPAM et le
   reste de vpc) reste, sous la règle de preuve réseau : sous OVN
   l'affirmation est vérifiée, ailleurs elle est sautée, et aucun document ne
   dit « isolé » sans nommer le mode.
5. **Le stockage sur les deux starters** : **Outscale fait, Exoscale
   ouvert.** OSC-4 est mergée (volumes, snapshots, images, la chaîne de
   stockage dans la fixture Terraform). EXO-4 (#12, stockage bloc) reste,
   aligné sur les règles de relation que Scaleway a réglées : stocké d'un
   côté, calculé de l'autre, règles de suppression testées par le destroy de
   la fixture.
6. **Load balancing et gateways** : **ouvert.** SW-5 (#17), SW-6 (#18), OSC-5
   (#16), EXO-5 (#14), EXO-6 (#15). En dernier parce que rien d'autre n'en
   dépend et que tout ce dont ils dépendent (IPAM, réseaux, la discipline des
   waiters) est au-dessus ; chacun est plan de contrôle d'abord, adossement
   sous capacité ensuite.

Les vagues sont un ordre, pas un calendrier : une vague peut démarrer avant que
la précédente soit toute verte quand ses dépendances le sont, et une issue où
un client officiel casse passe devant toute la liste, comme le dit la dernière
section.

### La vue opérationnelle

Une ligne par lot. Chaque ligne est une issue, pilotée depuis le
[tableau de projet](project.md) ; une issue fermée est la preuve que la ligne
est faite, donc le tableau porte l'état comme référence d'issue plutôt que
comme affirmation.

| ID | Vague | Livre | État |
|---|--:|---|---|
| **X-1** | 1 | `Declined()` porte une raison par opération | fait |
| **SW-1** | 1 | iam et marketplace sous le gate ; instance/vpc/ipam triés | fait (#4) |
| **OSC-1** | 1 | la moitié non-IaaS déclinée nommément | fait (#3) |
| **EXO-1** | 1 | la surface des services managés déclinée nommément | fait |
| **OSC-2** | 2 | `ProductCodes`, mot de passe admin, tags, volume racine : le premier `terraform apply` Outscale | fait (#6) |
| **EXO-2** | 2 | cycle de vie, groupes de sécurité, IP élastiques : l'étiquette *preview* tombe | fait (#5) |
| **SW-2** | 3 | snapshots, images, attachement de volume | fait (#7) |
| **SW-3** | 3 | `block/v1` et le volume racine `sbs_volume` | faite (#8) |
| **OSC-3** | 4 | réseau routable : `examples/net_vm` applique | fait (#10) |
| **SW-4** | 4 | cycle de vie IPAM et le reste de vpc | ouvert (#11) |
| **EXO-3** | 4 | réseaux privés et attachement d'instance | faite (#9) |
| **OSC-4** | 5 | volumes, snapshots, images | fait (#13) |
| **EXO-4** | 5 | stockage bloc | ouvert (#12) |
| **SW-5** | 6 | `lb/v1` ZonedAPI | ouvert (#17) |
| **SW-6** | 6 | `vpcgw/v2` | ouvert (#18) |
| **OSC-5** | 6 | load balancing | ouvert (#16) |
| **EXO-5** | 6 | NLB | ouvert (#14) |
| **EXO-6** | 6 | VPC et routes | ouvert (#15) |

Les tailles et les listes d'opérations restent avec les issues et les
documents archivés, où on peut les discuter contre les mesures qui les
justifiaient. La seule chose qui vaille d'être sue ici : SW-5 est le plus gros
lot restant.

### Commencer ici

Trois commandes, dans cet ordre, avant d'écrire du code :

```bash
mise run upstream:sync                 # le scan lit un checkout à jour ou rien
mise run drift:check                   # 0 : les baselines sont à jour ; 2 : trier avant de planifier
feint coverage --sdk .upstream/scaleway-sdk-go --products instance,vpc,ipam --format triage
```

La troisième imprime la liste de travail non triée des produits nommés, celle
que la vague 1 a vidée : pour ceux-ci elle lit zéro aujourd'hui, et une
nouvelle opération amont est exactement ce qui la fera cesser de lire zéro. Si
`drift:check` sort en 2, ce triage passe avant tout ce qui est sur cette
page : une baseline sur laquelle personne n'a statué rend vide de sens chaque
« la colonne lit zéro ».

### Quand un lot est fait

Les quatre mêmes conditions partout, dans l'ordre où elles échouent le plus
vite :

1. `mise run check` passe : gofmt, vet, golangci-lint, `go test -race`.
2. Chaque nouvelle route déclare son `Route.Operation` amont, et
   `TestEveryRouteDeclaresAnOperation` le prouve.
3. `mise run conformance` passe, y compris la preuve nouvelle du lot. Un test
   unitaire seul ne clôt rien : seul un vrai client pilotant la route le fait.
4. `tools/drift/gate.sh check` rend 0, avec les opérations du lot servies ou
   déclinées **avec une raison**, jamais laissées non triées.

Un lot qui satisfait 1, 2 et 4 mais pas 3 n'est pas fait ; c'est une forme que
personne n'a montrée à un vrai client.

---

## L'outillage qui rend la séquence moins chère

Les vagues sont le produit. Cette section est l'outillage autour, et elle
existe à cause d'une comparaison : la grille de fonctionnalités de LocalStack,
offres payantes comprises, a été lue contre ce projet. L'essentiel de cette
grille n'est pas transposable, et la raison est mesurée plutôt que ressentie :
ses fonctionnalités différenciantes (injection de chaos, flux de politiques
IAM, inspecteur de trafic) ont été bâties sur une couverture quasi complète,
là où les tableaux générés du README montrent encore une minorité de la
surface servie et des colonnes non triées pas toutes à zéro. Copier la grille
maintenant serait un deuxième étage sur des fondations encore coulées, et
chaque fonctionnalité ajoutée est une surface de plus à tenir contre un amont
qui bouge par centaines d'opérations chaque année.

Les chiffres ne sont volontairement pas répétés ici, pour la raison dite en
tête de page : un compte figé dans la prose pourrit, et les tableaux se
régénèrent. Les issues liées plus bas portent les nombres tels que mesurés le
jour où elles ont été écrites, ce qui est le rôle d'une issue.

La question utile n'est donc pas *quelles fonctionnalités de LocalStack
manquent ici*. C'est **lesquelles abaissent le coût de la couverture**,
puisque la couverture est ce à quoi la séquence ci-dessus passe son temps.
Trois répondaient oui, et la première a depuis été livrée.

### 1. Enregistrer ce que se disent un vrai client et un vrai cloud : #72 fait ; #73 et #74 restent

La moitié enregistrement est **livrée** : `feint proxy` enregistre une
transcription expurgée d'un vrai client contre un vrai cloud,
`feint transcript --shape` la réduit à un arbre de champs versionnable (sans
valeurs, sans identifiants), et depuis #122 le gate de formes compare les
réponses de l'émulateur à ces formes observées **sur chaque pull request**,
`DeclinedFields()` portant les refus motivés. La phrase sur laquelle cette
section se terminait (« l'outil qui a produit les mesures les plus précieuses
de ce dépôt est le seul qui n'a jamais été construit ») est réglée, et l'outil
a gagné sa place dès son premier jour branché : le gate est passé au rouge sur
une vraie divergence (`images[].default_bootscript`) dans la branche même qui
rendait la route comparable (#131).

Ce qui reste de la famille, chacun utilisable sans l'autre : **`feint replay`**
(#73) compare la réponse de l'émulateur à celle que le vrai cloud a donnée,
échange par échange ; **`feint coverage --observed`** (#74) ordonne la colonne
non triée par ce qu'un client a réellement été vu appeler, ce qui transforme
chaque vague ci-dessus d'un pari en un comptage. Et un piège mesuré gouverne
les deux : un client qui suit des endpoints donnés dans la réponse quitte le
proxy en pleine session (#92), donc un enregistrement n'est complet que ce que
le dialecte permet.

**Preuve :** comme l'énonce chaque issue. La règle qui gouverne la famille a
tenu et tient : une transcription ne contient ni credential ni secret du
corps, prouvé par un test qui échoue quand l'appel d'expurgation est retiré ;
l'enregistrement se fait sur la station d'un humain, contre son propre compte,
jamais en CI.

### 2. L'émulateur comme paquet importable : #75

`Server.Handler() http.Handler` existe déjà ; tout ce qui l'utiliserait est
sous `internal/`, donc rien hors de ce module ne le peut. Deux éléments de
cette page le paient aujourd'hui : le module testcontainers doit démarrer une
image publiée pour atteindre un handler qui pourrait être un appel de
fonction, et « un quatrième provider ne change rien dans `internal/core` »
est admis plus bas comme non testé, comme il doit le rester : trois packs dans
un même arbre peuvent partager une erreur un an sans s'en apercevoir.

Un seul commit répond aux deux, parce que les deux ont besoin que les mêmes
types cessent d'être internes. C'est aussi la seule chose qu'un concurrent en
forme de conteneur ne peut pas copier : un conteneur ne devient pas un appel
de fonction dans le binaire de test de quelqu'un.

Le coût est réel et sa place est à côté du bénéfice : ce qui quitte
`internal/` devient une API avec laquelle ce projet cassera des gens. Décider
*quels* types, c'est décider combien de liberté future on vend ; la réponse
est le plus petit ensemble qui passe la preuve, pas l'ensemble qui a l'air
propre.

**Preuve :** un module hors de celui-ci, sans directive `replace`, démarre
l'émulateur in-process et le pilote avec le SDK Scaleway officiel ; et ce même
module définit un `Pack` à lui, sans rien importer sous `internal/`. La
seconde moitié est la première vraie preuve d'une affirmation d'architecture
que cette page fait depuis le début.

### 3. Injection de fautes : #26

Déjà ouverte, et elle reste troisième. Elle n'abaisse pas le coût de la
couverture, donc elle gagne sa place sur un autre argument : c'est un
middleware au-dessus du `ServeMux`, cela coûte peu, et ce que cela produit,
ce sont des mesures sur le comportement des clients officiels (le provider
Terraform Scaleway réessaie-t-il vraiment un 429, le waiter d'`exo`
converge-t-il sur une opération asynchrone lente), c'est-à-dire la matière
première de ce projet. Personne ne peut tester cela aujourd'hui sans dégrader
un vrai compte.

Elle se compose aussi avec le premier élément : une transcription enregistrée
porte un vrai 429 avec le corps que le cloud a réellement envoyé, ce qui
répond par la mesure à la question que cette issue laisse ouverte sur la
forme d'une erreur injectée.

### L'arbitrage à rouvrir : interception DNS et terminaison TLS : #76

Pas un quatrième élément de file. Un refus dont le coût n'a jamais été mesuré,
ce qui est un autre défaut, et un que ce dépôt prend au sérieux partout
ailleurs.

[limits.md](limits.md) décline l'object storage parce que le provider
Terraform Scaleway construit `https://s3.<region>.scw.cloud` dans le code :
le rediriger demande une interception DNS plus un certificat que le provider
accepte, « un projet à part entière ». La première moitié est une mesure et
elle est juste. La seconde est une estimation que personne n'a faite, et
l'exigence de la règle 3 (un refus porte une raison) n'est pas satisfaite par
une taille que personne n'a pesée.

Ce qui rouvre la question n'est pas une envie d'object storage. C'est que le
bloqueur est générique : **tout client dont l'endpoint est construit dans le
code plutôt que lu d'un réglage** est inatteignable pour la même raison, et ce
projet ne sait pas combien il en existe. Ce nombre pourrait trancher dans un
sens comme dans l'autre.

**Cette page ne tranche pas.** Ce que #76 demande, ce sont quatre mesures,
avant que quiconque écrive un résolveur : combien d'endpoints codés en dur
existent dans les providers Terraform, CLI et SDK des trois fournisseurs, et
quelles opérations chacun coûte ; ce qu'il faut à chaque client pour accepter
un certificat frappé localement, avec la ligne nette entre une variable
d'environnement limitée à une commande et une installation dans le magasin de
confiance du système de l'opérateur ; s'il faut un serveur DNS du tout, ou si
une entrée hosts ou un drapeau de résolveur couvre les cas mesurés ; et ce que
la bibliothèque standard donne gratuitement, une CA locale étant bon marché en
`crypto/x509` quand un serveur DNS n'a aucune réponse standard.

**Preuve :** [limits.md](limits.md) remplace « un projet à part entière » par
ces nombres et un verdict. Refusé, et cela rejoint « Non prévu » avec une
prose de qualité `Declined()`. Retenu, et cela devient un élément d'ici avec
un client officiel nommé d'avance. Chaque réponse clôt la question ; l'état
présent, un refus posé sur un coût non mesuré, est le seul qui ne la clôt pas.

### Considéré dans la même passe, et non mis en file

Nommé plutôt que laissé flotter, la même discipline que `Declined()` :

- **L'état d'amorçage déclaratif** (#77), un fichier de fixture qu'un relecteur
  peut lire là où un snapshot est un blob généré. Réel, petit, et derrière les
  trois ci-dessus. Son format est JSON, pas YAML : il n'y a pas d'analyseur
  YAML dans la bibliothèque standard et un `go.mod` de trois lignes ne se
  dépense pas pour rendre une fixture plus jolie.
- **La génération de politique de moindre privilège** : dériver une politique
  IAM Scaleway ou EIM Outscale des opérations qu'un run a été observé
  demander. C'est l'idée produit la plus forte de la comparaison, elle observe
  sans vérifier (donc ne dérange pas la décision de ne jamais contrôler les
  signatures), et l'observateur détient déjà l'essentiel des données. Elle
  attend parce que c'est de la différenciation, et une différenciation bâtie
  sur une surface aussi mince est une démonstration.
- **Le contrôle déterministe des temps de transition** : plus une note sur
  #26 : c'est **#124**, un ordonnanceur avancé par observations qui rend un
  état transitoire atteignable sans horloge murale, pour que les refus qui y
  vivent puissent tirer. Déposée depuis la revue externe ; le cas Outscale
  mesuré (`409 InvalidVolumeState`) est son ancre.
- **Une TUI d'inspection.** Supplantée pour l'instant par la page en lecture
  seule que le binaire sert sur lui-même (#67, #68, #69), qui montre les mêmes
  données. À revoir seulement si cette page s'avère être la mauvaise surface.
- **Un site public de couverture par provider.** Les tableaux sont déjà
  générés par `feint docs` ; en faire un site statique est un travail
  d'acquisition, pas de couverture, et il est ordonné en conséquence.
- **Les frontières multi-projet et multi-organisation.** Consignées ici
  surtout pour corriger la prémisse : `resource.Tenant` porte déjà un Project,
  et le pack Scaleway scope déjà clés SSH, groupes de sécurité, volumes et IP
  par le `project_id` que le client envoie. Ce qui est réellement figé est
  `organization_id`, et les serveurs ne sont pas scopés par projet. C'est donc
  un travail plus petit et mieux délimité qu'il n'y paraît, et il attend
  encore.

---

## La piste des preuves : ce que deux revues externes ont changé

Deux passes d'une revue externe (13 août 2026) ont été triées en issues comme
on trie un rapport de dérive : vérifiées contre l'arbre d'abord, enrichies ou
refusées avec une raison, jamais recopiées. Le verdict qui a survécu à la
vérification est celui vers lequel cette page penchait déjà : **le nombre de
routes démontre désormais l'architecture ; ce qu'il ne démontre pas encore,
c'est ce qu'un run vert prouve.** Dépenser les prochaines versions en preuves
plutôt qu'en surface est la proposition.

Les issues, chacune portant sa propre preuve :

- **#123** : ce qui est prouvé d'une opération devient un ensemble d'axes de
  preuve nommés (piloté, contrat, comportement, dataplane…), calculés depuis
  les artefacts, publiés sans jamais être additionnés en score.
- **#125** : la preuve adossée au runtime tourne sur une machine que personne
  ici ne possède ; elle promeut l'élément « Plus tard » ci-dessous et porte sa
  règle de promotion.
- **#130** : une page répond à ce qu'un utilisateur peut valider ici, par mode
  runtime, chaque ligne portant sa preuve ou sa limite.
- **#132**, **#133** : les surfaces contractuelles propres du projet (CLI,
  codes de sortie, `/_feint/*`, snapshots) gelées par des tests ; un snapshot
  est compris ou refusé, jamais à moitié lu en silence.
- **#134**, **#135** : des invariants de concurrence sous un barrage
  délibéré ; le comportement au crash et au redémarrage énoncé une fois et
  prouvé par un kill.
- **#124**, **#126**, **#128**, **#129** : états transitoires déterministes,
  catalogue strict opt-in, `feint exec`, signature épinglée au workflow de
  release. Fidélité et durcissement, explicitement derrière les preuves
  ci-dessus.

**L'arbitrage qu'elles proposent est #136**, et c'est une proposition, pas une
décision : la version 0.8 achète la *confiance* (CI runtime, concurrence,
déterminisme au crash, axes de preuve), la 0.9 achète le *contrat* (surfaces
gelées, snapshot v1, divergences lisibles par une machine, l'image OCI), la
1.0 achète l'*adoption* (la page de confiance, un exemple CI de référence,
`setup-feint`, l'engagement SemVer) ; et aucune version n'achète un nombre de
routes. La décision revient à l'auteur ; cette page consigne que la question
est posée, et que les vagues ci-dessus continuent quoi qu'il arrive.

---

## Ensuite : ce qu'un utilisateur demandera en premier

### Ce que « probed » et « refused » prouvent se resserre

Le côté requête a maintenant des dents : un champ envoyé par un client qu'aucun
handler ne lit fait échouer le run de conformance, le mécanisme qui a attrapé
un retype de serveur répondant 200 sans rien faire. Deux manques restent, tous
deux mesurés. Une sonde qui reçoit un 4xx compte comme *refused* et son corps
d'erreur n'est jamais validé contre le contrat, donc une mauvaise forme
d'erreur se cache derrière un bon code ; et les paramètres de requête ne sont
pas contractualisés, exactement là où un paramètre de taille de page ignoré
est passé jusqu'à ce qu'un vrai client s'en aperçoive. Un « probed » qui
prouve peu est pire qu'un manque honnête, parce qu'il se lit comme une preuve.

**Preuve :** le corps d'erreur d'une sonde refusée est validé contre le schéma
d'erreur du provider lui-même et une violation fait échouer
`mise run conformance` ; et les sondes des routes de liste font varier la
taille de page et vérifient la page reçue.

### IAM sous le gate de dérive : réglé

**Fait, avec SW-1.** `iam` est un produit scanné : il apparaît dans les
tableaux de couverture générés avec sa propre baseline, et une opération IAM
amont que personne n'a triée fait sortir `drift:check` en 2 comme pour
n'importe quel autre produit ; c'était la preuve énoncée. L'élément reste sur
cette page une release, parce que l'état qu'il a corrigé (servi et non mesuré,
l'état le moins défendable pour une route) mérite d'être rappelé par son nom.

### Une GitHub Action `setup-feint` et un modèle GitLab CI

Les verbes de cycle de vie ont été conçus pour la CI (`start`, `wait`, `env`,
codes de sortie stables), donc l'action est un composite mince, pas un projet.
Elle est listée après l'image parce que le chemin GitLab `services:` consomme
l'image, et parce qu'une action qui existerait avant que le scénario golden
image fonctionne installerait un outil qui échoue sur le premier module
réaliste.

**Preuve :** la CI d'un dépôt d'exemple, sur GitHub et sur GitLab, va du
checkout à un `terraform apply` vert contre l'émulateur en n'utilisant que
l'action ou le modèle publiés.

### Un module testcontainers-go

C'est ainsi qu'un émulateur entre dans les suites de test des autres. Il vit
dans un dépôt séparé, parce que le module doit dépendre de testcontainers
alors que le `go.mod` zéro dépendance de ce dépôt est imposé par un hook
pre-commit, et Go vient en premier parce que c'est la langue des SDK des trois
fournisseurs. Java et Python suivent le même motif seulement après que le
module Go a des utilisateurs.

**Preuve :** un `go test` dans le dépôt du module démarre l'image publiée,
pointe le SDK Scaleway officiel dessus, crée et supprime un serveur.

### Des suites de conformance découpées par ressource

Aujourd'hui un script par provider pilote tout ce que ce provider sert. Cela
fait grossir une suite sans limite et, plus concrètement, rend impossible d'en
faire tourner deux en parallèle sans qu'elles se disputent le même compte
émulé. La surface ne peut pas continuer de grandir si la suite qui la prouve
devient le goulot.

**Preuve :** les suites tournent en parallèle contre un seul émulateur et
passent.

---

## Plus tard : décidé, pas planifié

### `--vm` se fait prouver par la CI, sur un runner que personne ne possède : suivi par #125

**Livré, et c'est un job nocturne plutôt qu'un gate.**
`.github/workflows/runtime-proof.yml` installe Incus (canal Zabbly stable, clé
épinglée) et OVN sur un `ubuntu-24.04` hébergé par GitHub, câble la connexion
northbound, et exécute les suites réseau, ssh et crash dans les deux modes,
`incus` et `incus-ovn`. Les deux jambes sont passées, isolation entre VPC
affirmée, sur une machine que personne ici ne possède.

Ce qui reste vrai, et qui est la seule chose à lire comme une limite :
**aucun gate de pull request ne démarre un runtime de machines.** Le job est
consultatif tant que son critère de promotion n'est pas atteint, donc le mode
qui porte l'argument du produit n'est pas encore quelque chose qu'une pull
request doive satisfaire.

Ce paragraphe affirmait « aucun workflow de ce dépôt ne démarre un runtime de
machines » jusqu'à ce qu'un audit le confronte au workflow ajouté par le train
de livraison qui l'a lui-même embarqué. L'affirmation s'était propagée à cinq
endroits dans deux langues, et `docs --check` la reconduisait à chaque release,
puisqu'il compare la page à son générateur : il prouve la forme, jamais
l'énoncé. Vérifier n'est pas parser, commis sur la documentation de ce projet —
consigné ici plutôt que discrètement réécrit.

Il ne reste dans « plus tard » qu'au sens du calendrier ; le terrain est
mesuré (30 juillet 2026, sur la CI des projets amont eux-mêmes), et le tout
repose sur une seule combinaison non prouvée :

- **Incus tourne sur un `ubuntu-24.04` hébergé par GitHub.** `lxc/incus` y
  pilote son propre `test/main.sh` avec de vrais conteneurs sur zfs, btrfs,
  lvm et ceph.
- **OVN avec le datapath noyau y tourne aussi.** `ovn-org/ovn` exécute
  `system-test` (la variante qui charge `openvswitch.ko`, pas celle en espace
  utilisateur) sur le même runner, après
  `apt install linux-modules-extra-$(uname -r)` et un correctif du fichier
  hosts tiré de son `.ci/linux-util.sh`.
- **Personne ne fait tourner les deux ensemble.** La CI d'Incus ne contient
  aucune occurrence d'`ovn`, et le dépôt OVN n'a aucune suite de test Incus.

Un piège et une inconnue. Le piège est **AppArmor** : ce même
`.ci/linux-util.sh` exécute `aa-teardown` et désactive le service, ce qui se
lit comme un prérequis et n'en est pas un ; il cite un bug AppArmor d'Ubuntu
et le contourne pour des binaires compilés hors de tout profil packagé. Une
installation packagée n'en a pas besoin, mesuré : AppArmor chargé avec 180
profils, dont quatre d'Incus, et la suite réseau qui passe.
[install.md](install.md) le dit désormais, parce qu'un job qui recopierait la
recette amont en bloc désactiverait un contrôle d'accès obligatoire sur chaque
runner en appelant cela de l'installation. L'inconnue est **arm64** : aucun
des deux projets amont ne l'exerce sur un runner hébergé, donc un run vert là
serait la première preuve arm64 de ce dépôt, pas seulement de `--vm`.

L'ordre est : mesurer, puis gater. Le job atterrit derrière
`workflow_dispatch`, se lance à la main, et ne passe sur `pull_request` que
lorsque son taux d'échec nocturne sur un nombre de nuits énoncé atteint un
seuil énoncé ; un nombre que l'historique des Actions prouve, pas une opinion
(#125 consigne la règle). Un gate rouge le jour de son apparition est un gate
que tout le monde apprend à ignorer, et ce dépôt porte déjà la note sur ce que
cela coûte.

**Preuve :** un job de CI installe Incus depuis Zabbly plus OVN sur un runner
hébergé, câble la connexion northbound, et `FEINT_VM=incus-ovn` déroule la
suite réseau jusqu'au bout : le subnet créé à travers l'API émulée, l'adresse
publiée par l'API qui répond, et l'assertion d'isolation qui passe au lieu
d'être sautée. Jusque-là, la page d'installation dit que `--vm` est prouvé par
le tableau de release et jamais par la CI, et cette phrase est générée, donc
elle ne peut pas cesser d'être vraie en silence.

### Outscale rejoint Scaleway sur le cœur IaaS : en grande partie livré

Personne d'autre n'émule Outscale, et ses acheteurs (secteur public,
SecNumCloud) lisent des preuves plutôt que du marketing. L'essentiel de ce que
cet élément promettait est mergé : le plan d'adressage (Net, Subnet, bornes de
masque, contenance, chevauchement, un vrai bridge, une Vm portant l'adresse
publiée par l'API), le réseau routable avec le `examples/net_vm` du provider
qui applique, re-planifie vide et détruit (OSC-3), et la chaîne de stockage
(OSC-4). La barre de parité que cet élément nommait (cet apply `net_vm`) est
atteinte, et [limits.md](limits.md) consigne ce que la topologie servie fait
bouger et ne fait pas bouger.

Reste le load balancing (OSC-5, #16) et un avertissement qui a sa place ici
parce que c'est une décision d'architecture, pas du travail de pack : une
règle de groupe de sécurité sourcée par *groupe* plutôt que par CIDR demande
un sélecteur OVN, pas un jeu de règles statique, et cette question de modèle
réseau doit être répondue avant qu'un lot promette l'**application** des
groupes Outscale ; [limits.md](limits.md) dit qu'ils sont servis comme plan de
contrôle et non mesurés sur le trafic aujourd'hui.

**Preuve :** pour ce qui est livré, la suite de conformance telle qu'elle
tourne ; pour le reste, l'apply LB nommé dans OSC-5.

### L'object storage reste dehors, et le contournement gagne une page

La raison est énoncée et mesurée dans [limits.md](limits.md) : le provider
Terraform Scaleway code en dur `https://s3.<region>.scw.cloud`, donc le
supporter demande interception DNS et terminaison TLS plutôt qu'un réglage
d'endpoint. Émuler S3 n'est pas la partie difficile et ne l'a jamais été ;
atteindre l'émulateur l'est.

**Ce sur quoi ce « non » repose est désormais lui-même en question** : voir
l'arbitrage rouvert plus haut et #76. La mesure (l'endpoint est construit dans
le code) tient ; l'estimation qui l'a suivie (« un projet à part entière »)
n'a jamais été faite, et le bloqueur s'avère générique plutôt que propre à
l'object storage. Cet élément n'est donc plus un refus réglé, c'est un refus
en attente d'un coût.

Ce qui n'attend pas, c'est le « voici comment » : les chemins SDK et CLI
honorent `SCW_S3_ENDPOINT`, donc une page documentée feint plus MinIO couvre
le flux S3 pour tout sauf le chemin Terraform, ce que la page dit sans
détour. Cette page vaut d'être écrite quel que soit le sort de #76, parce que
c'est la réponse pour quiconque a besoin de S3 ce mois-ci.

**Preuve :** les commandes de la page sont exécutées en CI comme le sont
celles du README : `scw` pointé sur MinIO via `SCW_S3_ENDPOINT` dépose et
relit un objet.

### La compatibilité des snapshots entre versions : réglée par #133, mergée en #140

`feint snapshot` est livré, donc la question a cessé d'être hypothétique, et une
revue externe a forcé la mesure que cet élément attendait : le snapshot ne
portait **aucun champ de version**, et `store.Restore` décodait avec
`encoding/json` nu, donc un champ que cette version ne connaissait pas était
abandonné en silence et la restauration réussissait, exactement le best effort
que ce projet refuse partout ailleurs.

C'est désormais une enveloppe, `{"format": "feint-snapshot", "version": 1,
"resources": [...]}`, et `Restore` refuse ce dont il ne peut pas rendre compte :
un autre format, une autre version, un champ inconnu. Un tableau nu hérité est
reconnu comme tel et refusé par son nom, plutôt qu'au travers d'une erreur de
décodeur illisible. Faire bouger `snapshotVersion` est un changement cassant au
sens de RELEASING.fr.md, ce qui est précisément ce qui rend le champ utile.

Le cas adjacent que cet élément a toujours nommé (charger un snapshot pendant
qu'un runtime de machines tourne, ce qui remplace le store sans réconcilier les
machines réelles) appartenait à #135, close elle aussi, mergée en #141 : ce qui
survit à un émulateur mort est dit une fois, constaté au redémarrage, et prouvé
par un kill.

**Prouvée par :** la table de comportement que #133 réclamait, un test par
ligne, dans `internal/core/store` : `TestASnapshotOfThisVersionRoundTrips`,
`TestASnapshotFromTheFutureIsRefused`,
`TestALegacyBareArrayIsRefusedWithARemedy` et
`TestARestoredResourceKeepsItsIdentity`.

### Un quatrième provider

L'architecture a été construite pour qu'en ajouter un ne change rien dans
`internal/core`. Cette affirmation n'est pas testée : trois packs ne suffisent
pas à savoir si les coutures sont au bon endroit. Le quatrième se choisit par
la demande, pas par l'intuition. Le verrou qu'il attendait (Exoscale perdant
son étiquette *preview*) est levé depuis EXO-2, et
[fourth-pack.md](fourth-pack.md) a depuis mesuré ce qu'un tel pack toucherait,
fichier par fichier, remèdes classés ; les comptes vivent là-bas, où ils ont
été mesurés, pas ici. #75 porte la forme la plus forte du test : un pack hors
de l'arbre, qui ne peut pas compiler contre une couture mal placée, avec la
forme volontairement hostile que la revue a spécifiée.

**Preuve :** un nouveau pack est ajouté sans qu'une seule ligne change sous
`internal/core`.

### Paquets : Homebrew, nixpkgs, AUR

Urgence basse par choix : `go install` fonctionne aujourd'hui et un binaire de
release n'a besoin de rien. Les canaux s'ajoutent quand on les demande, et
aucun ne s'annonce sur parole.

**Preuve :** un canal d'installation n'apparaît dans le README qu'après qu'un
job de CI a installé depuis lui et piloté `scw instance server create` contre
le résultat.

---

## Non prévu, et pourquoi

Le dire à voix haute est la même discipline que `Declined()` dans les packs :
« non trié » et « hors périmètre » sont des réponses différentes, et seule la
seconde a sa place ici.

- **Les clouds américains.** Le vide européen est la douve : LocalStack existe
  pour AWS et Azurite pour Azure, et rien n'existait pour ces trois-là. AWS,
  GCP ou Azure noieraient aussi le mécanisme de dérive sous des milliers
  d'opérations et rendraient intenable l'affirmation « mesuré, pas suivi ».
  Les deux raisons sont structurelles, ce n'est donc pas une affaire de
  demande.
- **Une course au nombre de services.** Bases de données, plans de contrôle
  Kubernetes, serverless : chacun est un produit à part entière, et en faire
  un mal coûte plus de crédibilité que de ne pas le faire. Dix services de
  façade détruiraient la seule chose qui distingue ce projet, un data plane
  qui tient ses promesses.
- **Toute dépendance Go externe.** Un `go.mod` de trois lignes est un argument
  de sécurité pour un outil qui tournera dans la CI de tout le monde, et un
  hook pre-commit l'impose. Ce qui exige une dépendance (le module
  testcontainers ci-dessus) vit dans son propre dépôt.
- **La télémétrie, ou un compte. Jamais.** « Pas de compte, pas de facture »
  est dans la première ligne du README et cette phrase est porteuse.
- **Un quatrième provider avant que le troisième soit utilisable.** Sinon le
  résultat est trois vitrines à moitié vides au lieu de deux pleines.
- **L'image conteneur comme mode nominal.** L'auto-détachement est le seul
  point où Feint bat les trois émulateurs comparables à la fois ; un process
  JVM ou CPython ne sait pas se démoniser proprement, un binaire Go statique
  sait. L'image existe pour que l'émulateur entre dans l'outillage des
  autres, pas pour remplacer le binaire.
- **Vérifier qu'un identifiant existe, par défaut.** Un create nommant une
  image que l'émulateur n'a jamais vue réussit, sur les trois packs, là où les
  vrais clouds refusent. C'est la limite la plus susceptible de mordre et elle
  est délibérée, argumentée dans [limits.md](limits.md) : l'émulateur n'a pas
  d'inventaire, et une équipe qui pointe une configuration Terraform
  existante vers lui ne doit pas échouer sur un UUID d'image de production
  codé en dur. Ce qui a changé depuis : une image inconnue ne peut plus
  *démarrer un substitut* (le boot échoue en disant pourquoi), et un mode de
  validation **opt-in**, où un opérateur déclare son propre catalogue et se
  fait refuser ses fautes de frappe, est proposé en #126. Le défaut ne change
  jamais ; tout changement doit garder les UUID de production fonctionnels.
- **Émuler la console ou l'interface web d'un provider.** Le public pilote des
  API.
- **Facturation, quotas ou capacité.** Un émulateur n'a pas de capacité à
  rapporter. Là où un schéma exige un nombre, il est plausible et fixe, et
  [limits.md](limits.md) le dit plutôt que de faire semblant.
- **Un runtime de machines Docker.** Retiré délibérément : émuler un cloud,
  c'est émuler son réseau, ce qui demande un bridge sur un bloc choisi, une
  adresse fixe par interface dès le démarrage, et des règles réellement
  appliquées. Incus fournit les trois ; Docker en fournit une et demie. Les
  mesures sont dans [limits.md](limits.md). Le réintroduire, c'est d'abord
  répondre à cela.
- **Vérifier les signatures.** Chaque credential est accepté à dessein, pour
  que l'outil tourne sans compte. [SECURITY.md](../SECURITY.md) énonce la
  conséquence.

### Refusé après lecture de la grille d'un concurrent

Ajouté depuis la comparaison LocalStack qui a produit la section ci-dessus.
Ces éléments y existent, plusieurs derrière un paywall, et les nommer vaut
mieux que les laisser flotter comme des choses sur lesquelles personne n'a
statué ; la même raison pour laquelle `Declined()` prend une raison.

- **Instances éphémères hébergées et environnements de preview.** Leur Cloud
  Sandbox fait tourner l'émulateur sur la machine de quelqu'un d'autre,
  joignable par une URL, ce qui exige un compte et une facture. C'est une
  contradiction frontale avec la première ligne du README, et la
  contradiction est le fond, pas un détail d'emballage. Rien n'en devient
  acceptable à un autre prix.
- **Un opérateur Kubernetes, un chart Helm, ou un exécuteur côté cluster.** Le
  public est la station d'un développeur et un runner de CI, pas un cluster.
  L'image conteneur déjà en construction couvre le bloc `services:` et le
  fichier compose, ce que ce public demande réellement. Un opérateur serait
  maintenu pour une forme de déploiement que personne ici n'a demandée.
- **SSO, SCIM, espaces partagés, tableaux de bord d'usage.** Des
  fonctionnalités de sièges en entreprise. Elles monétisent un émulateur ;
  elles n'émulent rien. Une équipe qui veut un état partagé a `feint snapshot`
  et un fichier.
- **Une course au nombre de services**, redite parce que la comparaison rend
  la tentation concrète : bases managées, Kubernetes managé, serverless. Déjà
  refusée plus haut, et la grille est exactement la pression que ce refus
  existe pour tenir.

### Posé, non tranché : l'estimation de coût

Un élément de cette comparaison n'est ni retenu ni refusé, et prétendre le
contraire serait l'option malhonnête.

**Facturation, quotas et capacité sont refusés plus haut**, à juste titre : un
émulateur n'a pas de capacité à rapporter, donc tout nombre qu'il invente est
un mensonge avec un schéma autour. **Estimer ce qu'un `terraform plan`
coûterait** sur les grilles publiques des trois fournisseurs est une autre
question. Cela n'invente rien (les grilles sont publiées), cela répond à une
chose qu'un utilisateur veut réellement avant un apply, et rien ne l'outille
aujourd'hui pour les clouds européens.

C'est écrit ici parce que cela n'entre dans aucune des deux listes. La réponse
la plus probable est que c'est un produit adjacent et un binaire séparé plutôt
qu'un mode de celui-ci : il n'a besoin ni d'émulation, ni de store, ni de
runtime de machines, il lit un plan et une grille de prix, et l'intégrer
mettrait une table de prix dans un dépôt dont toute la discipline est que les
tables fixes sont de la fiction ([limits.md](limits.md) dit exactement cela du
catalogue). Adjacent, pas inclus ; mais c'est un penchant, pas une décision,
et cela reste ici jusqu'à ce que quelqu'un en prenne une.

---

## Ce qui changerait cette feuille de route

Une issue qui dit « ce client officiel fait une chose que l'émulateur ne sait
pas suivre » passe devant tout ce qui précède. L'ordre d'ici est un pari sur
ce dont les gens ont besoin ; un client qui casse est un fait à son sujet.
