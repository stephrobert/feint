# Journal des modifications

**Autre langue :** [English](./CHANGELOG.md)

Changements notables, au format [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/),
versionnés selon [Semantic Versioning](https://semver.org/lang/fr/).

> [!NOTE]
> C'est [CHANGELOG.md](./CHANGELOG.md) que lit le workflow de release, et c'est
> donc lui la source : la section correspondant à un tag devient le corps de sa
> GitHub Release. Cette page en est la traduction, mise à jour à chaque version.

Deux types de changement méritent leur propre ligne quelle que soit leur taille,
parce que c'est là-dessus que ce projet est jugé : **une forme de réponse qu'un
client peut observer**, et **une limite qui a bougé**. Une refactorisation qui ne
change ni l'un ni l'autre a sa place dans `git log`.

## [Non publié]

### Corrigé

- **Le tableau de statut est généré depuis le workflow qui le prouve** (signalé
  par un lecteur comparant le README à la CI). Il annonçait Outscale prouvé par
  `oapi-cli` et Terraform. Les deux étaient vrais du dépôt et un seul l'était de
  la CI : la fixture Terraform Outscale existe, applique vingt-et-une
  ressources, produit un second plan vide et détruit proprement — et aucun
  workflow ne la lançait, si bien qu'une régression y serait arrivée en release
  sans un seul rouge.

  La suite est en CI désormais, sur les deux moteurs, et le tableau n'est plus
  écrit à la main : sa colonne *proven by* est lue depuis
  `.github/workflows/conformance.yml`, donc un client y apparaît quand un
  workflow le pilote et en sort quand il cesse. Une suite exécutée par la CI que
  personne n'a nommée est refusée par son nom plutôt qu'omise en silence —
  sous-estimer ce qui est prouvé est le même défaut vu de l'autre côté.

- **Six défauts trouvés par l'audit du train 0.8, et le faux verdict qui en a
  révélé un septième.** La livraison a été auditée deux fois avant le tag ; tout
  ce qui suit a été reproduit avant d'être corrigé.

  - **Un volume block restauré plus petit que son snapshot répondait 201.** Le
    garde vivait sur `updateBlockVolume` alors que son commentaire énonçait une
    propriété du volume — « un volume grandit et ne rétrécit pas » — tenue sur un
    chemin sur deux. Un snapshot de 10 Go restauré dans un volume de 1 Go,
    `available`. Ni le barrage ni le balayage d'invariants ne pouvaient le voir :
    l'un ne pilote aucune route block, l'autre interroge l'identité, pas le sens
    d'une taille.
  - **La libération d'une adresse IPAM ne prenait aucun verrou**, quand la
    réservation tenait l'allocateur de la reconstruction jusqu'au stockage.
    `allocatorFor` reconstruit l'occupation depuis les seules ressources IPAM :
    en supprimer une est exactement ce qui libère une adresse. Les deux chemins
    de libération le tiennent désormais et relisent sous verrou, et les écritures
    passent par `Commit` plutôt que `Put`, qui réinsère une ressource que le
    client a relâchée.
  - **Une revendication de falsification était fausse.** « Neutralisez n'importe
    lequel des trois verrous et le barrage rougit au premier essai » : trente
    exécutions vertes sans le verrou de NIC privée. Chaque travailleur prend son
    propre subnet, donc aucun défaut de contention ne peut y apparaître, quoi
    qu'il pilote. La phrase dit maintenant lesquels deux il tient, et nomme le
    test qui tient le troisième.
  - **L'artefact de preuve avait quatorze opérations de retard** au moment de
    livrer, sans que rien ne soit rouge : `docs/routes.md` affichait « — » pour
    elles, ce qui se lit exactement comme une opération que rien n'a prouvée. Un
    test exige désormais que toute opération montée y ait sa ligne.
  - **Une assertion de conformance produisait un faux verdict.** `feint clean` a
    gagné une ligne signalant les enregistrements runtime périmés, imprimée avant
    son bilan ; la suite Outscale comparait le début de toute la sortie et
    annonçait « the delete left a machine behind » quand le bilan disait zéro.
    Elle lit le décompte, et refuse de trancher quand il n'y a rien à lire.
  - **La documentation affirmait qu'aucun workflow ne démarre de runtime**, à
    cinq endroits dans deux langues, démentie par le job nocturne livré dans le
    même train. L'affirmation était gelée dans le générateur, donc
    `feint docs --check` la reconduisait à chaque release : le gate compare la
    page à son générateur, il prouve la forme et jamais l'énoncé. La distinction
    vraie — aucun gate de pull request n'en démarre — est ce qu'ils disent
    désormais.

### Modifié

- **La recette de vérification nomme le workflow de release, pas le dépôt**
  (#129). `docs/install.md` publiait `--certificate-identity-regexp
  'https://github.com/stephrobert/feint/.*'`, qui accepte **n'importe quel**
  workflow de ce dépôt recevant un jour `id-token: write` — une affirmation sur
  qui possède le dépôt, non sur ce qui a construit le fichier. Elle est
  désormais ancrée sur `.github/workflows/release.yml@refs/tags/v`, et la
  recette `gh` gagne `--signer-workflow`.

  Les deux moitiés ont été exécutées contre la 0.7.3 publiée : la nouvelle
  identité la vérifie, et pointée sur un autre workflow du même dépôt elle
  refuse, en nommant ce qu'elle attendait et ce qu'elle a trouvé. L'ancienne
  acceptait cet autre workflow — c'est la largeur qu'on ferme.

  `tools/release/preflight.sh` extrait maintenant l'identité **depuis la page**
  et l'exécute contre la release précédente, puis vérifie qu'elle sait encore
  refuser. La recette qu'un lecteur copie et ce que le workflow signe ne peuvent
  plus diverger en silence, ce qui est précisément ce qui s'était produit.

- **Un barrage de trafic concurrent, et un balayage d'invariants sur le store**
  (#134). Chaque pack pilote désormais ses propres routes servies depuis dix
  travailleurs simultanés — le parallélisme par défaut de Terraform — puis le
  store est balayé par un contrôle neutre vis-à-vis des providers dans
  `internal/core/store/storetest` : aucun identifiant émis deux fois, aucune
  adresse détenue par deux ressources d'un même genre, aucun objet runtime
  revendiqué par deux ressources. Un restore de snapshot en pleine circulation a
  son propre test.

  **Il a trouvé un vrai défaut dès sa première exécution contre Exoscale** :
  l'allocation d'IP élastique était un lire-modifier-écrire sans verrou, si bien
  que trois adresses sur seize créations sont allées à deux ressources chacune.
  Le pack Scaleway avait corrigé cette forme pour ses propres pools depuis
  longtemps et celui-ci ne l'a jamais reçue — écrit deux fois, corrigé une fois,
  vivant dans l'autre copie.

  Le balayage vit dans le noyau et ne connaît aucun provider : un quatrième pack
  en hérite en appelant une fonction. Il reconnaît les adresses à leur forme et
  non au nom de l'attribut, parce qu'un balayage indexé sur l'orthographe de
  chaque pack est un balayage auquel un pack nouveau échappe.

- **Un snapshot est compris ou refusé, et dit de quelle version il est** (#133).
  `feint snapshot save`, `GET /_feint/state` et `--state` écrivent désormais
  `{"format": "feint-snapshot", "version": 1, "resources": [...]}` au lieu d'un
  tableau nu, et `Restore` refuse tout ce dont il ne peut pas rendre compte :
  une version qu'il ne lit pas, un format qui n'est pas le nôtre, un champ
  inconnu sur une ressource.

  **C'est un changement cassant des formats d'état et de snapshot.** Un fichier
  écrit par une 0.7.x est refusé, avec le message qui dit comment le convertir,
  et un `PUT /_feint/state` portant un tableau nu l'est aussi. Reprendre un
  snapshot depuis l'instance qui détient l'état est la voie de sortie.

  Pourquoi cela valait de casser : l'ancien format perdait des données en
  silence. Un snapshot portant un champ que ce binaire ne déclare pas se
  restaurait *avec succès*, `encoding/json` jetait le champ, et la sauvegarde
  suivante réécrivait le fichier sans lui. Le store était cohérent, faux, et
  vert — mesuré avant le changement, pas redouté. `snapshot.go` documente ce
  format comme fait pour survivre à son instance et être chargé dans une autre,
  ce qui est précisément le moment où cela mord.

  `Attrs` reste ouvert, parce que ses clés sont des données qu'un pack a
  choisies et non un schéma : un attribut nouveau n'est pas un changement de
  format, et le refuser rendrait cassant tout ajout dans un pack.

  Deux documents se contredisaient sur la nature de cette promesse —
  `store.go` qualifiait le format de détail d'implémentation sans engagement de
  compatibilité, `RELEASING.md` le classait parmi les surfaces dont le
  changement est cassant. Ils disent maintenant la même chose, et c'est le plus
  strict qui l'emporte.

### Ajouté

- **Une image de conteneur, plan de contrôle seulement, publiée avec la
  release.** Le workflow de release pousse `ghcr.io/stephrobert/feint:<tag>`
  pour linux/amd64 et linux/arm64 : les binaires exacts que la release signe,
  enveloppés dans `scratch`, 16,6 Mo, rien d'autre à l'intérieur. Elle exécute
  `feint serve` avec `--vm off` et le dit : les vraies machines restent une
  propriété du binaire sur un hôte Incus, et une image qui prétendrait le
  contraire serait la demi-vérité que ce projet refuse. La preuve n'est pas
  « elle démarre » : le job `image` du workflow de conformance pilote
  l'émulateur dans le conteneur avec le CLI officiel `scw` et la sonde de
  contrats à chaque pull request, à travers le port publié, comme un bloc
  `services:` l'atteint. L'image est signée (keyless, par digest) et porte des
  attestations de provenance et de SBOM sous la même identité
  `release.yml@refs/tags/v` que les binaires ; la recette de vérification de
  docs/install.md est exécutée par le workflow de release contre l'image qu'il
  vient de pousser, et par le preflight contre la release précédente. Un tag
  par release, rien de mutable : pas de `latest`. Une release refuse désormais
  de pousser une image dont le propre `feint version` n'est pas le tag publié.

- **Scaleway sert le Block Storage, et le volume racine d'un serveur devient
  enfin écrivable** (#8). `block/v1` et `block/v1alpha1` montent 22 routes :
  volumes, snapshots et catalogue de types. `root_volume { volume_type =
  "sbs_volume" }` est honoré, le disque est créé dans Block, et le provider
  Terraform le relit par le repli qu'il a toujours utilisé —
  `instance.GetVolume` d'abord, puis `block.GetVolume` sur un 404 typé.

  Avant cela, `root_volume` n'avait aucune valeur utilisable : le provider
  refuse `b_ssd` d'emblée depuis la 2.79, et `sbs_volume` produisait un diff
  permanent. Mesuré et signalé par @vde-dis, qui a tenté de l'honorer, vu
  l'apply mourir sur « waiting for Volume failed: http error 404 Not Found », et
  jeté le changement plutôt que de deviner. La fixture de conformance omettait
  le bloc : la suite était verte sans jamais poser la question — un test qui
  évite la seule entrée qui casse. Elle déclare le bloc désormais.

  Deux choses que seuls les vrais clients pouvaient dire, trouvées au premier
  passage de la suite. `scw` 2.56.3 appelle `/block/v1alpha1` pour **toutes**
  ses commandes block quand le provider Terraform appelle `/block/v1` : deux
  clients officiels d'un même cloud, épinglés chacun à une orthographe, donc les
  deux sont servis par les mêmes handlers plutôt qu'une refusée sur une raison
  que le CLI dément. Et `scw block volume create` refuse un nombre d'octets brut
  là où la commande instance l'accepte, exigeant `10G`.

  La forme du volume vient d'un enregistrement d'un vrai compte et non du SDK,
  d'où `kms_key_id` et `last_detached_at` présents et nuls, et un `references`
  calculé depuis l'attachement plutôt que stocké à côté. L'identifiant d'une
  référence dérive de la paire qu'il relie, donc il relit à l'identique à chaque
  plan.

  Refus qu'un client peut observer : un volume créé sans `from_empty` ni
  `from_snapshot` ou avec les deux, un volume qui rétrécirait, un `kms_key_id`
  nommant un Key Manager que cet émulateur ne sert pas, un volume supprimé sous
  son serveur, un snapshot supprimé sous un volume qui en est issu. Les deux
  transferts Object Storage restent refusés, pour la raison que porte
  `instance/v1.ExportSnapshot`.

  Conformance : **158 routes sur 206 prouvées par un vrai client**, contre 146
  sur 184.

- **Scaleway sert les snapshots et les images que le client crée** (#7). La
  séquence de l'image dorée — snapshoter un volume, y tailler une image, lister
  puis supprimer les deux dans l'ordre que l'API impose — est un parcours de
  plan de contrôle auquel on peut répondre à chaque étape, et Outscale y répond
  depuis la 0.6.0 pendant que ce pack le refusait. Neuf opérations passent de
  `Declined()` à servies : `Create/Get/List/Update/DeleteSnapshot`,
  `Create/Update/DeleteImage` et `ListImages`, qui porte désormais les images du
  client à côté du catalogue fixe.

  Les refus qu'elles remplacent disaient « un snapshot copie les octets d'un
  volume » et « le catalogue est une table que rien ne peut agrandir ». Les deux
  phrases étaient vraies et aucune n'était une raison : les octets sont la seule
  part qu'un émulateur ne peut pas fournir, et cette limite est nommée là où
  elle mord plutôt qu'à l'entrée — une image taillée ici ne démarre rien et le
  dit (#115), au lieu de substituer une distribution que personne n'a demandée.
  `ExportSnapshot` reste refusée, puisqu'elle écrit ces octets dans Object
  Storage.

  Deux refus qu'un client peut observer : le snapshot d'un volume qui n'existe
  pas rend un 404 plutôt que l'enregistrement de rien, et un snapshot dont une
  image est tirée refuse de disparaître tant que l'image est là — l'ordre que
  Terraform parcourt quand un même plan retire les deux. `GET /images/{id}` lit
  l'image du client avant le catalogue, sans quoi une création et la lecture qui
  la suit décrivaient deux objets différents.

  `scw instance snapshot create`, `image create`, `image list` et l'ordre de
  suppression sont pilotés de bout en bout par la suite de conformance : 146
  routes sur 184 sont maintenant prouvées par un vrai client, contre 140.

### Corrigé

- **`feint --help` nomme toutes les commandes qu'il dispatche** (signalé par
  @vde-dis). `shapes` répondait, était documentée dans le guide, et manquait à
  l'aide : qui explorait le CLI comme on le fait ne pouvait pas la trouver. La
  ligne est ajoutée, et un test lit désormais le dispatch depuis la source et
  exige que chaque verbe y figure : ajouter l'entrée manquante corrige
  aujourd'hui, comparer les deux listes est ce qui empêche le vingt-et-unième
  verbe de partir invisible.

## [0.7.3]

### Corrigé

- **L'adresse qu'un client lit répond désormais, sur les trois providers et
  dans les deux modes de runtime** (#116, #117). Un serveur portant une IP
  flexible publiait une adresse pendant que la machine en portait une autre, ou
  aucune : le routage était enregistré avant que la machine n'existe et n'était
  jamais rejoué au démarrage, et une machine sans NIC privée démarrait sur le
  pont du profil de l'opérateur, où poser une règle de pare-feu rebranchait
  l'interface et lui coûtait son bail DHCP. Un `ssh` vers l'adresse publiée
  expirait donc pendant que le conteneur tournait et répondait à ses voisins.

  L'adresse qu'un client lit reste celle du provider — TEST-NET-3, -2 et -1, un
  bloc par pack pour que deux clouds émulés sur un même hôte ne routent jamais
  la même /32 — et elle est réellement routée, dès le premier démarrage, sur un
  réseau que l'émulateur possède. Publier l'adresse DHCP du runtime a été
  écarté : cela désynchronise `server.public_ips[].address` de `GET /ips/{id}`,
  ce que le vrai cloud ne fait jamais, et cela varie avec `--vm` et avec l'hôte.

  `dynamic_ip_required` était décodé, réécrit dans la réponse et lu par
  personne ; il alloue désormais une adresse éphémère, relâchée à l'arrêt.
  Parce que le champ *était* lu, il n'apparaissait jamais dans
  `unread_request_fields`, le seul angle mort du gate de contrat.

- **Le login est prouvé sur les trois packs, plus affirmé sur deux.** `ssh.sh`
  n'existait que pour Scaleway : `outscale` et le `default-user` du template
  Exoscale n'étaient que des noms dans une table que personne ne pilotait. Il y
  a trois suites désormais, chacune enregistrant sa clé par l'API de son
  provider et ouvrant une vraie session, et chacune échoue au lieu de se sauter
  quand un runtime est présent — celle de Scaleway se sautait, et c'est ainsi
  qu'une adresse cassée est partie sous un run vert.

- **Ce qu'OVN ne sait pas faire est déclaré plutôt qu'écrit en limite.** Une
  adresse interne à un subnet répond à l'hôte en mode pont et pas en OVN, parce
  que le routeur qui sépare deux VPC fait aussi du SNAT sur les connexions de
  l'hôte au retour — mesuré, sshd vivant et répondant à ses voisins pendant que
  l'hôte lisait le port comme fermé. `capabilities.private_from_host` dit
  lequel, de sorte qu'une sonde se saute avec un motif au lieu d'échouer, et le
  plan public routé traverse dans les deux modes : c'est par lui que passe la
  chaîne ssh de chaque pack.

- **Un produit Scaleway entier répondait `404 page not found` en texte brut.**
  Signalé par @vde-dis sur #74, mesuré sous un vrai apply OpenTofu :
  `/lb/v1/…` et `/vpc-gw/v2/…` tombaient dans le mux par défaut de net/http, et
  le SDK Scaleway lit d'abord le type de contenu puis jette un corps qui n'est
  pas du JSON. L'appelant recevait donc `404 Not Found` et rien d'autre, alors
  que tous les autres refus répondent dans le dialecte du provider.

  La liste de préfixes ne déclarait que les cinq produits servis par ce pack,
  quand l'espace d'URL de Scaleway en compte soixante-deux. Elle déclare
  désormais l'espace plutôt que la part servie, extraite des chemins de requête
  générés du SDK et non de ses noms de répertoires, qui diffèrent : le SDK dit
  `vpcgw`, l'URL dit `/vpc-gw/`.

  Le garde censé l'empêcher ne pouvait pas le voir.
  `TestEveryRouteFallsUnderADeclaredPrefix` parcourt les routes que le pack
  monte : un produit sans **aucune** route lui est invisible par construction,
  alors que son commentaire affirmait empêcher « qu'un produit entier réponde en
  texte brut ». Falsifié : la liste ramenée à cinq, le nouveau test échoue et
  celui-là passe toujours, ce qui démontre l'angle mort au lieu de l'affirmer.

- **Un identifiant d'image inconnu ne démarre plus un substitut** (#83). Avec
  un runtime configuré, les trois packs remplaçaient en silence une image
  qu'aucun catalogue ne connaît : demander Alpine, obtenir Ubuntu, pendant que
  l'API continuait d'afficher l'identifiant envoyé par le client ; la
  résolution Scaleway comparait les labels par sous-chaîne, si bien que
  `centos`, `rocky` et `ubuntu_focal` devenaient tous Ubuntu 22.04. La création
  réussit toujours, comme `docs/limits.md` le promet aux identifiants de
  production codés en dur, mais le démarrage refuse désormais : la machine
  atteint l'état d'échec propre au provider et le journal nomme l'identifiant.
  L'image et le login se résolvent maintenant ensemble (root chez Scaleway,
  `outscale` chez Outscale, le `default-user` du template chez Exoscale) et la
  marketplace Scaleway répond un UUID fixe par label, de sorte qu'un label
  résolu par Terraform nomme toujours la distribution choisie. Une image
  enregistrée par le client via `CreateImage` (Outscale) refuse de la même
  façon, l'émulateur n'ayant derrière elle qu'un enregistrement et aucun
  contenu de disque, et le journal dit lequel des deux cas s'applique.

### Sécurité

- **Deux installations de CI prenaient la dernière version disponible.**
  `pipx install uv`, dans le workflow de dérive hebdomadaire, n'était épinglé à
  rien — et c'est le job qui régénère des artefacts versionnés et ouvre une pull
  request avec eux ; commitizen, lui, l'était par version et non par empreinte.
  Les deux installent désormais depuis un fichier d'exigences avec empreintes,
  et `TestTheWorkflowsPinTheSameToolsAsMise` échoue dès qu'une version y cesse
  de correspondre au fichier qui la possède : `mise.toml` pour uv, le hook
  pre-commit pour commitizen, de sorte qu'un poste et un runner ne peuvent pas
  exécuter des outils différents. Signalé par Pinned-Dependencies d'OpenSSF
  Scorecard.

- **`SECURITY.md` porte une adresse joignable.** Il décrivait comment signaler
  sans lier quoi que ce soit : il fallait savoir où GitHub range le formulaire.
  Il nomme désormais l'URL de l'avis, et une seconde voie pour qui n'a pas accès
  à GitHub.

## [0.7.2]

### Ajouté

- **140 routes sur 175 sont prouvées par un vrai client**, contre 132. Les huit
  sont celles qu'OSC-3 et OSC-4 nommaient parmi leurs livrables et qu'aucun
  client n'avait jamais pilotées : les quatre opérations NIC, `ReadNatServices`,
  et les trois mises à jour que Terraform ne parcourt pas parce qu'il remplace au
  lieu de mettre à jour — `UpdateRoute`, `UpdateVolume`, `UpdateImage`. Les deux
  lots (#10, #13) se ferment là-dessus, et non sur des opérations servies.

### Corrigé
- **Une interface réseau attachée rapportait un état de lien que le provider
  Terraform refuse.** `LinkNic.State` publiait `in-use`, qui est l'état de
  l'*interface* ; l'état du *lien* est `attached`, et le provider interroge
  `ReadNics` jusqu'à le lire. Il abandonnait sur `unexpected state 'in-use',
  wanted target 'attached, detached, failed'`, laissant un apply à moitié fait
  et un destroy incapable d'aboutir. Le même fichier rendait `attached` pour la
  NIC primaire d'une Vm quarante lignes plus haut — un champ, deux orthographes
  — et le test unitaire qui affirmait `in-use` verrouillait la mauvaise, sous un
  nom prétendant correspondre à un enregistrement, alors que `feint shapes`
  enregistre des arbres de champs et jamais des valeurs.

- **Un enregistrement pouvait s'arrêter tôt sans rien dire.** Certaines API
  remettent une adresse au client dans un corps de réponse — Exoscale publie un
  `api-endpoint` par zone — et un client qui la suit quitte le proxy pour tout
  ce qui vient après. Mesuré sur un vrai compte : une session valant environ
  quatre-vingt-dix échanges en a enregistré **huit**, et la transcription avait
  l'air complète. `feint proxy` compte désormais les réponses qui nomment un
  hôte autre que celui auquel le client s'adresse, nomme ces hôtes à la fin du
  run, et dit franchement que ce qui est parti là-bas est absent du fichier.

  Détecté par la forme plutôt que par le nom du champ — une URL absolue dont
  l'hôte n'est pas celui du client — parce que nommer `api-endpoint` mettrait le
  vocabulaire d'un provider dans un outil qui n'en porte aucun, et la prochaine
  API à faire cela serait de nouveau silencieuse. Deux propriétés portent leur
  propre test : une réponse qui nomme le proxy lui-même n'est **pas** un renvoi,
  parce que la liste de zones de l'émulateur pointe sur elle-même et qu'une
  alarme qui sonne sur le cas normal finit ignorée ; et un corps gzippé est
  décompressé avant le scan, parce que `scw` et `exo` envoient tous deux
  `Accept-Encoding` et qu'un scan sur des octets compressés ne trouve rien d'une
  façon qui se lit « il n'y a rien ».

  Il ne réécrit pas l'adresse, délibérément : un enregistreur qui modifie ce
  qu'il enregistre n'en est pas un. `docs/proxy.md` énonce désormais ce qu'un
  enregistrement peut promettre pour les trois providers plutôt que pour le seul
  Outscale — l'un refuse bruyamment, l'un tronquait en silence, l'un enregistre
  entièrement (#92).

## [0.7.1]

### Corrigé

- **`CreateTags` affirmait qu'une ressource que l'émulateur venait de créer
  n'existait pas.** Signalé par Vincent Dislaire sur un
  `outscale_internet_service` portant un bloc `tags` : l'apply échouait sur
  `the resource igw-… does not exist`, à propos d'une ressource que
  `ReadInternetServices` servait. La table de préfixes de `tags.go` avait quatre
  entrées, écrites quand le pack servait quatre familles, et 0.6.0 en a ajouté
  dix sans y toucher. Trois préfixes étaient signalés ; lire quels schémas
  déclarent `Tags` en a trouvé **dix** — volumes, snapshots, images, groupes de
  sécurité, tables de routage, IP publiques, NIC, options DHCP, services NAT et
  services internet.
- **Deux des quatre valeurs `ResourceType` que publiait `ReadTags` étaient
  inventées** : `net` là où le SDK d'Outscale dit `vpc`, `vm` là où il dit
  `instance`. Un client qui filtrait sur `instance` ne trouvait rien. Aucun
  contrat ne pouvait le voir — leur OpenAPI déclare `ResourceType` comme une
  simple chaîne — et un test unitaire affirmait `net` depuis trois versions, ce
  qui est la façon dont l'erreur d'un émulateur devient ce que sa propre suite
  protège.

  Les valeurs viennent désormais de `TagResourceType` dans `osc-sdk-go`, et un
  test épingle chaque ligne de la table à cette énumération.
- **La table qui a causé tout cela ne peut plus prendre du retard en silence.**
  Chaque préfixe d'identifiant que le pack émet est lu depuis le source et doit
  être trié : étiquetable avec son type upstream, ou refusé avec une raison — la
  discipline que `Declined()` applique aux opérations. Ajouter les dix lignes
  manquantes corrige aujourd'hui ; la onzième ressource aurait recommencé.

### Modifié

- **Exoscale est *starter*, plus *preview*.** Le label avait été pris
  délibérément, avec une condition de sortie écrite — *tant que le provider
  Terraform n'est pas prouvé contre lui* — et cette condition reposait sur une
  hypothèse que la mesure a réfutée : le provider honore
  `EXOSCALE_API_ENDPOINT` pour son client egoscale v3 et en construit un v2 sans
  aucune option d'endpoint, si bien qu'un apply se scinde entre cet émulateur et
  un compte payant. `ClientOptWithAPIEndpoint` existe dans egoscale et n'est
  jamais appelée ; trois sites construisent un client v2 sans elle. Remontée en
  amont sous
  [exoscale/terraform-provider-exoscale#573](https://github.com/exoscale/terraform-provider-exoscale/issues/573).

  Une condition que personne ici ne peut atteindre n'est pas une condition,
  c'est un otage — et ce contre quoi le label mettait en garde a de toute façon
  été corrigé par EXO-2 : `exo` pilote stop, start, reboot, scale, resize, une
  suppression refusée sur une instance protégée, un aller-retour de règle de
  groupe de sécurité, un groupe d'anti-affinité, et une IP élastique attachée,
  publiée puis retirée. Ce qui sépare encore Exoscale d'*usable* reste dans les
  tableaux de couverture générés, où cela ne peut pas flatter : 75 opérations
  non triées, contre 18 pour Outscale et 0 pour Scaleway.

- **La ligne Outscale crédite Terraform**, qui pilote dix-sept ressources de
  bout en bout depuis 0.6.0 et n'était toujours créditée qu'à `oapi-cli`.

## [0.7.0]

### Ajouté

- **`feint shapes` enregistre ce qu'un vrai cloud renvoie et vérifie l'émulateur
  contre cela.** L'arbre des champs uniquement — chemins et types JSON, aucune
  valeur et aucun identifiant — ce qui le rend versionnable là où une transcription
  ne l'est pas : une transcription décrit le compte de quelqu'un, une forme décrit
  une API. L'enregistrement demande un vrai compte et reste le travail d'un humain ;
  `feint shapes --check` compare hors ligne, sans aucun credential, et rapporte
  combien il a comparé, pour qu'un vert ne se lise pas « rien ne va mal » quand il
  veut dire « rien n'a été vérifié ».
- **`internal/upstream`, un seul endroit qui sait parler à un vrai cloud** —
  signature, cadence et réessai pour les trois providers, répondant avec le même
  `trace.Exchange` qu'écrit le proxy. Sept copies de la signature Outscale
  s'étaient accumulées dans des scripts jetables, et la différence entre deux
  d'entre elles a coûté une heure : l'une signait le chemin de la requête, l'autre
  `/`, et seul le cloud pouvait les distinguer. Chaque signature vient désormais de
  la source propre à son provider.
- **Exoscale sert un cycle de vie** : démarrage, arrêt, redémarrage, changement de
  taille et suppression d'instance, groupes de sécurité et leurs règles, groupes
  d'anti-affinité, IP élastiques et leur attachement — de 16 à 46 opérations
  servies, de 108 à 75 non triées. Chaque chemin de cycle de vie passe par
  `Binding.Serialise`, avec un test de concurrence sous `-race`.
- **131 routes sur 175 sont prouvées par un vrai client**, contre 109 sur 145.

### Corrigé

- **`data.outscale_images` provoquait un segfault du provider Terraform.** Signalé
  par Vincent Dislaire, qui l'a tracé dans les sources du provider lui-même : trois
  champs lus sans garde nil dans une boucle où chaque voisin en survit un, et le
  catalogue n'en publiait aucun. Il a mesuré le correctif plutôt que de le deviner,
  en injectant un `BlockDeviceMappings` vide à travers un proxy et en regardant le
  crash se déplacer au champ suivant, puis au suivant, puis s'arrêter.
- **Des champs que les vrais clouds renvoient et que ceux-ci ne renvoyaient pas**,
  trouvés en comparant la réponse de chaque pack à un enregistrement plutôt que
  par un client qui casse : onze sur chaque produit serveur Scaleway — dont
  `capabilities.placement_groups`, que ce pack sert et qu'un client vérifiant
  d'abord la capacité aurait lu comme non supportée —, douze sur `ReadImages`
  d'Outscale, trois sur `ReadVmTypes`, sept sur le `template` d'Exoscale.
- **Seize routes Exoscale déjà servies répondaient la mauvaise forme** :
  `instance-type` et `template` à l'intérieur d'une instance sont des références
  nues `{id}`, pas des entrées de catalogue développées, et un test unitaire
  verrouillait cette erreur parce qu'il affirmait le schéma plutôt que le fil.
- **Un en-tête dont personne ne s'est porté garant n'est plus écrit.** La
  rédaction correspondait à huit sous-chaînes de noms, et les trois dialectes
  servis ici ne passaient que parce que leurs porteurs s'appellent `Authorization`
  et `X-Auth-Token` — une coïncidence, pas une règle. Reproduit : `X-Auth-Token`
  rédigé et `X-Consumer` portant la même valeur écrite en clair. Les en-têtes de
  requête et de réponse sont désormais une liste d'autorisation ; les corps
  restent une liste de refus, parce qu'un corps **est** la mesure et qu'un en-tête
  ne l'est pas.

### Modifié

- **Le type de volume racine Scaleway a deux raisons, et l'une manquait.** Le
  commentaire à côté de `rootVolume` justifiait de forcer `b_ssd` par un argument
  qui ne couvre que les volumes locaux : il ne disait donc rien de `sbs_volume` —
  et un lecteur a levé la restriction sur cette base. `docs/limits.md` énonce
  désormais la conséquence franchement : aucun type de `root_volume` n'est
  inscriptible aujourd'hui, `b_ssd` ne planifiera plus à partir du provider 2.79,
  `sbs_volume` ne planifie jamais, et omettre le bloc est la voie de passage. Cela
  clôt le #8. Signalé par Vincent Dislaire.
- **`docs/fourth-pack.md`** mesure ce qu'un quatrième pack de provider toucherait :
  environ 45 lignes additives réparties sur 13 fichiers partagés, et aucun code
  dans `internal/core` nommant un provider. La règle du noyau neutre tient sous la
  mesure plutôt que par affirmation.

## [0.6.0]

### Ajouté

- **Les familles routage et stockage d'Outscale**, pilotées de bout en bout par le
  vrai provider Terraform : groupes de sécurité et leurs règles, tables de
  routage, routes et leurs liaisons, interfaces réseau, services internet,
  services NAT, IP publiques et leurs liaisons, snapshots, et images qu'un client
  enregistre à côté du catalogue fixe.
  `tools/conformance/outscale/terraform.sh` applique désormais l'`examples/net_vm`
  du provider lui-même plus la chaîne de stockage — dix-sept ressources — avec un
  second plan vide et un destroy propre. Le score de conformance est passé de 88 à
  109 routes sur 145 prouvées par un vrai client.
- **Vingt champs sur `ReadVms` que le vrai cloud renvoie et que l'émulateur ne
  renvoyait pas**, dont `Nics`, `Placement`, `Architecture`, `BootMode`,
  `RootDeviceType` et `PrivateDnsName` ; `LinkPublicIp` sur une interface ; et
  `SnapshotId` sur un volume taillé depuis un snapshot. Trouvés en enregistrant un
  vrai compte et en le diffant par opération contre l'émulateur — une classe de
  défaut qu'aucun contrat ne peut voir, parce que les schémas d'Outscale ne
  déclarent presque aucun champ requis (#88).

### Modifié

- **Les filtres qu'un vrai client envoie sont appliqués plutôt que refusés**, sur
  les tables de routage (`RouteDestinationIpRanges`, les filtres de liaison), les
  IP publiques (`LinkPublicIpIds`), les machines et interfaces
  (`SecurityGroupIds`) et les volumes (`LinkVolumeVmIds`, qui est la façon dont le
  provider attend un attachement et un détachement). Chacun était un 400 qui
  arrêtait un vrai apply ou destroy en cours de route.
- **`ReadLoadBalancers` répond une liste vide** au lieu d'être décliné. Le reste de
  la famille reste décliné : décliner une lecture dont la réponse honnête est
  « aucun » coûte à un client la possibilité de demander et n'achète aucune
  honnêteté.

### Ajouté (outillage)

- **`feint proxy`**, un reverse proxy qui se place entre un vrai client officiel et
  un vrai cloud et consigne chaque échange en JSON Lines d'
  `internal/trace.Exchange` — la forme même que publie l'anneau de l'émulateur.
  Les credentials n'atteignent jamais le fichier : la rédaction est une propriété
  du type enregistré, pas une étape qu'un site d'appel peut oublier. C'est ainsi
  que ce projet cesse de deviner ce qu'un client envoie et le mesure à la place, et
  le premier passage réel a enregistré une vraie trouvaille — `scw` appelle
  `GET /block/v1alpha1/zones/{zone}/volumes/{id}`, qu'aucun pack ne sert. Boucle
  locale uniquement sauf `--expose-to-network`, parce que chaque requête qui le
  traverse porte un credential vivant.
- **`feint transcript`**, qui transforme un enregistrement de proxy en les trois
  réponses dont un développeur a besoin avant de servir une opération de plus, pour
  que le fichier s'interroge par un verbe plutôt qu'en sachant où chaque fait se
  trouve dans le JSON :
  - sans drapeau, **les opérations qu'un vrai client a appelées et qu'aucun pack ne
    sert**, classées par nombre d'appels puis par taille de réponse — la file de
    travail, dérivée d'une mesure au lieu de la supposition de la roadmap ;
  - `--shape <opération>`, **l'arbre des champs que le vrai cloud a réellement
    renvoyé**, qui n'est pas ce que le SDK dit qu'il peut renvoyer ;
  - `--shape <op> --against <emulator.jsonl>`, **les champs que le vrai cloud
    renvoie et que l'émulateur omet ou type différemment** — un défaut de forme de
    réponse qu'aucun test unitaire ne peut voir, trouvé avant que le handler ne
    soit écrit plutôt qu'après. Mesuré contre Outscale, cela a rapporté que le
    `ReadVolumes` de l'émulateur omet `SnapshotId` et ne peuple jamais
    `LinkedVolumes`.

## [0.5.0]

### Ajouté

- **Une page que le binaire sert sur lui-même**, à `/_feint/ui`, ouverte par
  `feint ui`. Servie sur l'interface de boucle locale uniquement — hors boucle
  locale elle n'est pas montée du tout —, en lecture seule, sans authentification
  par conception, et sans aucune dépendance : trois fichiers embarqués dans le
  binaire, pas d'étape de build, pas de framework. Elle montre servi contre piloté
  contre sondé sans jamais les additionner, l'écart versionné avec l'API upstream
  de chaque provider, tout ce que la session a créé, et un journal en direct des
  appels. Chaque agrégat s'ouvre sur ce dont il est fait.
- **`GET /_feint/resources`**, un inventaire lu depuis le store plutôt que depuis
  une API de provider, pour qu'un pack que personne n'a encore écrit soit listé
  avec son propre vocabulaire. Des attributs entiers, jamais un sous-ensemble
  choisi. C'est le seul point d'entrée qui publie `Runtime` — le conteneur derrière
  une machine — et un test pilote toutes les routes de lecture des providers pour
  prouver qu'aucune ne le fait.
- **`GET /_feint/events` et `GET /_feint/trace`**, le journal des appels : un
  anneau borné de 256 échanges, en flux d'événements unidirectionnel pour la page
  et en tableau JSON pour un script ou un job de CI qui le lit après coup. Chaque
  ligne porte l'heure, le statut, la durée, l'opération, les champs qu'un client a
  envoyés et qu'aucun handler n'a lus, et les champs qu'une réponse portait et que
  la description d'API du provider ne définit pas. Les deux étaient déjà calculés
  et affichés nulle part.
- **La forme de l'enregistrement est `internal/trace.Exchange`**, publiée une fois
  pour que la transcription qu'écrira `feint proxy` (X-2) et le rejeu qui la lit
  (X-3) partagent un format avec l'anneau propre à l'émulateur.
- **Les artefacts de couverture portent leurs verdicts par opération**, avec la
  raison pour laquelle chaque opération déclinée l'a été. 625 arguments qui
  n'étaient auparavant atteignables qu'en lançant un scan contre un clone de SDK.

- **La page est vérifiée dans un vrai navigateur, et photographiée par le même
  run.** `mise run docs:ui` la charge contre un émulateur vivant, attend ses
  données, et affirme dix-huit valeurs contre ce que les points d'entrée ont
  répondu — les comptes, une ressource créée et ses attributs, une raison de refus
  en entier, un appel journalisé et le champ qu'aucun handler n'a lu — avant
  d'écrire les images que le README montre. Cela tourne sur chaque pull request.
  `docs/limits.md` dit ce que cela couvre et ce que cela laisse de côté ; la
  version courte est qu'un nœud renommé fait désormais échouer la CI et qu'une
  feuille de style laide, toujours pas.
- **Les captures d'écran sont sur le rail de fraîcheur existant.**
  `feint docs --check` compare une empreinte de la page à celle enregistrée à côté
  des images, de sorte qu'un changement de la page sans régénération fait échouer
  le hook pre-commit, le gate de docs et le préflight de release. Il compare la
  page, jamais les pixels : la page rend des valeurs d'horloge, donc une
  comparaison octet à octet serait rouge pour toujours.

### Modifié

- **`/_feint/health` répond `capabilities: null`** quand le pilote de machine ne
  déclare rien, au lieu d'un objet de cinq `false`. Le silence et le refus étaient
  indiscernables sur le fil, si bien qu'un lecteur affichait « non » au nom d'un
  pilote à qui l'on n'avait jamais demandé. `feint status` dit désormais
  `isolation: not declared` dans ce cas. Un pilote qui déclare — tous ceux qui
  sont livrés aujourd'hui, y compris celui qui ne fait rien — est inchangé.
- **`/_feint/conformance` porte `probes`**, les comptes de sonde par opération, à
  côté de `calls`. Le scalaire `probed` pouvait dire combien de routes seule une
  sonde avait atteintes, jamais lesquelles.

## [0.4.1]

### Corrigé

- **`feint clean` ramasse les répertoires d'état que personne ne ramassait.**
  Mesuré sur une station de développement après une journée : quatorze répertoires
  sous `XDG_RUNTIME_DIR` pour deux émulateurs vivants, dont douze ne décrivaient
  plus rien du tout. `stop` efface l'enregistrement et laisse le répertoire qui
  porte le journal — ce qui est juste en soi, puisqu'un plantage se lit après coup
  ou pas du tout — mais rien ne les balayait, de sorte que le répertoire d'état
  cessait d'être lisible comme réponse à « qu'est-ce qui tourne ». Un émulateur
  vivant n'est jamais touché, quel que soit son âge.
- **`feint clean --vm off` sort désormais avec 0.** Il balayait, il le disait, et
  il sortait avec 1, parce que le balayage n'était atteignable qu'à travers un
  runtime de machines. `off` est le défaut de `serve` et la majorité des runs :
  c'était donc le chemin courant plutôt qu'un cas limite — et un succès qui sort
  comme un échec est l'ambiguïté que ce projet refuse partout ailleurs. Un runtime
  qui ne peut réellement pas être balayé échoue toujours.

  Le message `nothing was left behind` gagne `on the runtime`, et est désormais
  affiché dans tous les modes. `tools/conformance/scaleway/network.sh` décide que
  le runtime est propre en cherchant cette ligne, et il ne doit pas changer de
  réponse parce qu'un répertoire a été ramassé.

### Modifié

- **La roadmap porte la file issue de la comparaison de ce projet avec
  LocalStack**, offres payantes comprises, et les refus que cette comparaison a
  imposés. Trois éléments passent devant un lot, sur un seul filtre — lesquels
  d'entre eux abaissent le coût de la couverture : enregistrer ce qu'un vrai client
  et un vrai cloud se disent, l'émulateur comme paquet importable, et l'injection
  de fautes. Le refus d'intercepter le DNS et de terminer TLS est rouvert comme
  question plutôt que tranché : la mesure sur laquelle il repose tient, l'estimation
  de coût qui l'a suivie n'a jamais été faite.
- **`CONTRIBUTING.md` dit comment se lit un titre d'issue.** Un défaut est titré
  par le symptôme, parce que le diagnostic est souvent faux quand l'issue est
  ouverte et que le titre lui survit ; une unité de livraison porte son code de
  lot, qui est ce par quoi un commit la ferme en le nommant.

## [0.4.0]

### Ajouté

- **Terraform pilote Outscale.** `init`, `validate`, `plan`, `apply`, un second
  plan vide et `destroy`, contre le provider publié `outscale/outscale` v1.7.0.
  Jusqu'ici tout ce que ce pack revendiquait était prouvé par `oapi-cli` seul, et
  le provider emprunte des chemins qu'aucun CLI n'emprunte. Écrire la fixture a
  suffi à trouver six défauts, chacun listé ci-dessous.
- **Les volumes.** Créer, lire, mettre à jour, supprimer, lier et délier. Un volume
  grandit et refuse de rétrécir — un système de fichiers ne survit pas au
  rétrécissement de son disque — et un volume lié refuse de partir, ce dont a
  besoin un client qui détruit dans le mauvais ordre pour pouvoir réessayer. Les
  snapshots restent déclinés : il n'y a pas d'octets derrière un volume émulé, donc
  en restaurer un produirait un disque qui ne contient rien.
- **Les tags sur chaque ressource qui en porte**, triés, parce que leur ordre est un
  diff Terraform permanent qui n'attend que de se produire. Et `ReadVmsState`, la
  vue légère qu'un client interroge en boucle.
- **74 routes sur 104 sont prouvées par un vrai client**, contre 69 sur 93. 23 de
  plus ne sont que sondées : le protocole tient et le comportement n'est pas
  prouvé.

### Corrigé

- **`terraform apply` mourait sur la première machine.** Le catalogue ne publiait
  aucun `ProductCodes`, si bien que le provider appelait `ReadAdminPassword` sur
  chaque Vm qu'il relisait, Linux compris — c'est un appel Windows, et une liste
  absente se lit « inconnu ». Cette route n'existait pas. Les images et les Vms
  publient désormais le code Linux, et l'appel répond un mot de passe vide, jamais
  un mot de passe généré : un credential inventé est un credential qu'un client
  pourrait essayer d'utiliser.
- **Chaque `terraform destroy` faisait planter le provider net** — « Plugin did not
  respond », reproduit avec le provider publié et signé v1.7.0. `DeleteVms`
  supprimait l'enregistrement, et le provider répond à une suppression en
  interrogeant `ReadVms` jusqu'à ce que la Vm rapporte `terminated` : une liste
  vide n'est pas un état que son attente connaît. Une Vm terminée reste désormais
  lisible, comme en amont, et ne détient rien — elle est ignorée par la garde du
  Subnet et par le compte d'adresses.
- **Le destroy échouait ensuite sur la paire de clés**, avec « the keypair  does
  not exist » et un trou là où l'id devrait être : le provider crée par nom et
  détruit par id, et le pack ne lisait que le nom.
- **Quatre champs que le provider envoie et que le pack ne déclarait nulle part**,
  le pire étant `DeletionProtection` — accepté et jeté, ce qui disait à un client
  que sa machine était protégée alors que rien ne la protégeait. Également
  `NestedVirtualization`, `ResultsPerPage` sur trois lectures, et `ForceStop`.
- **`feint status` rapportait 0 dans la colonne « piloté par un client », toujours.**
  `internal/cli` déclarait sa propre forme pour `/_feint/conformance`, avec une clé
  que le serveur n'émet nulle part et `untouched` en objet là où le fil porte un
  tableau. Le décodage échouait, les deux appelants retombaient sur vide, et le
  commentaire d'en-tête de `status.go` promettait exactement ce nombre. Mesuré
  après deux appels `scw` : 0 avant, 2 après. La copie est supprimée plutôt que
  corrigée — la vue est désormais un seul type exporté que les deux côtés lisent.

### Modifié

- **`feint serve` refuse une adresse hors boucle locale** sauf si
  `--expose-to-network` en décide autrement. Hors boucle locale, la garde
  anti-rebinding cesse de refuser quoi que ce soit — à raison, puisqu'elle ne peut
  plus dire ce qui est local — et la seule sortie était
  `feint dev listening on 0.0.0.0:4599`. Mesuré : une page cross-origin et un
  `Host` falsifié obtiennent tous deux un 200 là où ils obtiennent un 403 sur
  127.0.0.1. Avec `--vm` activé, ce qui était alors joignable depuis le réseau est
  un runtime de conteneurs.

  C'est un changement de comportement qu'un utilisateur peut observer. Qui exposait
  délibérément le port ajoute un drapeau ; qui ne l'exposait pas exposait plus
  qu'il ne le savait.

## [0.3.3]

### Corrigé

- **Une instance Exoscale pouvait être créée sans aucun des champs que l'API
  exige**, et une telle instance faisait cesser de lister `exo compute instance
  list` : chaque instance créée après elle disparaissait de la sortie du CLI
  officiel.
- **Une clé SSH enregistrée n'atteignait jamais la machine à laquelle elle était
  attachée.** Le pack gardait un nom et une empreinte et jetait la clé elle-même,
  si bien que l'instance démarrait sans utilisateur et sans moyen d'entrer, tandis
  que l'API publiait une adresse dessus.
- **Une clé envoyée comme `ssh-key` était acceptée et jetée.** Leur API documente à
  la fois `ssh-key` et `ssh-keys`, aucun des deux déprécié ; le pack ne lisait que
  le pluriel.
- **Rien n'était vérifié à l'entrée Exoscale** : des noms et des clés portant des
  caractères de contrôle étaient stockés et restitués verbatim.
- **`exo limits` répondait 404.** C'est une commande de premier plan du CLI
  officiel, et les routes de quota n'étaient ni servies ni refusées.

### Ajouté

- **Les quotas, comptés plutôt qu'inventés.** La limite est une affirmation que cet
  émulateur fait, comme son catalogue ; l'usage est un fait qu'il détient, compté
  depuis le store. `exo limits` rapporte zéro instance sur un émulateur frais et
  une après une création, et la suite de conformance vérifie exactement cela.
- **69 routes sur 93 sont prouvées par un vrai client**, contre 68 sur 91.

### Modifié

- **Le provider Terraform Exoscale est refusé, avec une explication.** Il honore
  `EXOSCALE_API_ENDPOINT` pour la moitié de ses appels et atteint le vrai cloud
  avec l'autre moitié, si bien qu'un apply se répartissait entre cet émulateur et
  un compte payant — mesuré, sans qu'un octet quitte la machine. Servir un client à
  moitié est pire que le refuser : un demi-succès est indiscernable de « ça marche »
  jusqu'à la facture. `FEINT_EXOSCALE_ALLOW_TERRAFORM=1` lève le refus pour qui
  comprend la répartition, et `docs/limits.md` porte tout le raisonnement. Le CLI
  `exo` est inchangé.

  C'est un changement de comportement qu'un utilisateur peut observer, et c'est un
  patch plutôt qu'un mineur à dessein : ce qui cesse de fonctionner ne fonctionnait
  pas — cela créait des ressources sur un vrai compte tout en ayant l'air local.

## [0.3.2]

### Corrigé

- **`FEINT_VM=incus-ovn mise run conformance` ne pouvait pas tourner du tout.** La
  suite réseau Outscale construisait un nom de binaire à partir du nom du mode et
  cherchait `incus-ovn`, qui n'existe pas — donc le seul mode qui livre l'isolation
  entre deux VPC était celui qu'on ne pouvait pas vérifier de bout en bout. Il
  passe désormais, les deux suites réseau comprises.
- **Une machine virtuelle ne portait jamais l'adresse que l'API publiait** avec
  `--vm incus-vm`. Quatre causes : l'interface était ajoutée pendant que la machine
  démarrait encore, ce qui échouait par intermittence ; l'invité nomme ses
  interfaces autrement que le runtime, si bien que l'adresse était appliquée à un
  nom qui n'existe pas à l'intérieur ; l'adresse matérielle générée n'est pas
  stockée là où le pilote la cherchait ; et quand l'attachement échouait malgré
  tout, la NIC privée continuait de répondre `available`. Elle répond désormais
  `syncing_error`, pour qu'un client apprenne ce que le journal savait.
- **Tous les filtres sauf un étaient ignorés** sur les lectures Outscale, et
  répondaient l'inventaire entier avec un 200 — indiscernable d'un succès, si bien
  qu'un script qui supprime ce qu'un filtre a trouvé supprimait tout. Les filtres
  sont désormais appliqués ou refusés avec le champ nommé ; les Vms filtrent sur
  ids, états, images, types, paires de clés, subnets, nets et adresses, les Nets et
  Subnets sur ids, plages et états, les paires de clés sur noms, empreintes et
  types.
- **L'empreinte de clé SSH ne correspondait à rien qu'un client puisse calculer.**
  Elle était prise sur toute la ligne de clé, commentaire compris, au lieu de la
  clé décodée : elle différait donc de ce qu'affiche `ssh-keygen -l -E md5` et
  changeait quand seul le commentaire changeait. `KeypairType` répondait aussi
  `ssh-rsa` pour chaque clé, y compris les ed25519, et une clé dont le matériau
  n'est pas du base64 valide était acceptée — ce qui démarre une machine portant
  des octets qu'aucun démon SSH ne lira.
- **`UpdateVm` acceptait ce que `CreateVms` refuse** : un user data au-delà du
  plafond de 500 Kio, et une paire de clés qui n'existe pas — la seconde démarre
  une machine dans laquelle personne ne peut se connecter tandis que l'API affirme
  qu'une clé est attachée.
- **Un 200 dont l'écriture était perdue** : `UpdateVm` ne prenait pas de verrou par
  cible, si bien qu'un démarrage concurrent écrasait ce qu'il venait de rapporter
  comme enregistré, et Terraform reproposait le même changement à chaque plan.
- **Un Subnet pouvait atterrir dans un Net déjà supprimé**, laissant un pont sur
  l'hôte sous rien, et un `terraform apply` créant dix subnets les sérialisait les
  uns derrière les autres parce que le verrou d'adressage était tenu pendant
  l'appel au runtime.
- **Une Vm arrêtée hors d'un Subnet perdait son adresse privée.** Outscale la garde
  jusqu'à ce que la machine soit terminée.
- **Une requête de plus d'1 Mio était tronquée avant que le handler ne la voie**, et
  revenait en erreur de syntaxe à propos d'un document que le client avait envoyé
  entier.
- **`DryRun: false`, une requête légitime, faisait échouer le gate de conformance
  du projet lui-même.**

### Ajouté

- **`docs/limits.md` dit ce que fait `DryRun` ici** : il est répondu avant qu'aucun
  handler ne tourne, donc rien ne se produit — la moitié qui compte pour un hôte —
  et il ne valide pas, donc un dry run d'une requête malformée répond 200 ici et
  400 en amont. Le code citait cette section depuis deux versions avant qu'elle
  n'existe.
- **Les suites de conformance pilotent ce qu'elles n'avaient jamais piloté** : un
  filtre, un `DryRun`, et une vraie clé SSH dont l'empreinte est vérifiée contre la
  valeur qu'affiche `ssh-keygen`. Chaque défaut ci-dessus vivait dans l'écart entre
  la suite et l'affirmation.

## [0.3.1]

### Corrigé

- **La suite de conformance Outscale mesurait ce qui répondait sur 4599**, quel que
  soit le port qu'on lui demandait de piloter : `oapi-cli` laisse l'environnement
  l'emporter sur `--config`, et le fichier de credentials épinglait
  `OSC_ENDPOINT_API`. L'argument d'endpoint de la suite était inerte, donc un run
  pouvait rapporter sur un autre serveur que celui sous test — ou sur rien. La
  garde qui refuse de tourner quand cette variable n'est pas définie existait et
  n'était appelée que par la suite Scaleway.
- **`scw instance server update <serveur> volumes.0.id=<racine d'un autre serveur>`
  répondait 200** et déplaçait le volume : les deux serveurs le listaient ensuite,
  et la racine propre au serveur patché était détachée en silence. Le contrôle de
  propriété vivait dans la couche partagée et l'un de ses trois appelants jetait le
  verdict.
- **Une création dont la ressource disparaissait en plein démarrage laissait une
  machine en marche**, avec un runtime configuré — invisible pour le plan de
  contrôle, donc rien ne l'arrêterait jamais. Atteignable via `PUT /_feint/state`,
  que le format de snapshot documente comme un chemin supporté.
- **`feint coverage` donnait à quatre opérations de lecture de snapshot une raison
  qui décrit une création**, ce qui est le défaut « vrai de la famille, faux du
  membre » que les raisons existent pour prévenir.

### Ajouté

- **`TestEveryCitedTestExists` indexe les citations par paquet.** Un commentaire
  nommant un test qui vit dans un autre paquet n'est accepté que s'il dit lequel ;
  un homonyme ailleurs ne satisfait plus une citation qui ne pointe sur rien. Il
  joint aussi les lignes de commentaire avant la comparaison, pour qu'une citation
  répartie sur deux lignes soit vue.

### Modifié

- **Le préflight de release déduit à nouveau la version des commits.** Il
  rapportait « commitizen is not installed » sur la machine qui publie les
  releases, où le hook pre-commit commitizen tourne à chaque commit depuis un
  environnement qui n'est pas dans le `PATH` ; v0.3.0 a donc été taguée sur un
  numéro déduit à la main. Il lance désormais commitizen via `uvx`, épinglé à la
  version qu'utilise le hook. Vérifié après coup : les commits impliquaient bien
  0.3.0.

## [0.3.0]

### Modifié

- **`Declined()` porte une raison pour chaque refus**, et le rapport de couverture
  l'affiche. Un refus était auparavant un nom d'opération nu dont la justification
  vivait dans un commentaire que seul un lecteur du code voyait, ce qui rendait
  « pas encore trié » et « hors périmètre » indiscernables de l'extérieur. Ce sont
  deux réponses différentes et une seule est un refus. Rupture pour qui implémente
  `emulator.Pack` : `Declined() []string` devient `Declined() []Decline`.
- **Un refus sans raison exploitable arrête le serveur**, plutôt que d'être
  rapporté plus tard. Les chaînes vides, `TODO`, `n/a`, une raison de moins de cinq
  mots, et une raison qui n'est que le nom de l'opération redit sont toutes
  refusées au démarrage avec le code de sortie 1. Le gate existe pour rendre
  visible la surface non triée ; une raison bouche-trou est la façon dont il cesse
  de fonctionner.

### Ajouté

- **La surface upstream des trois providers est triée.** Exoscale passe de 358
  opérations sur lesquelles personne n'avait décidé à 110, Outscale de 199 à 96, et
  les `iam` et `marketplace` de Scaleway entrent sous le gate de dérive — servi et
  non mesuré est le pire état dans lequel une route puisse être. Chaque refus est
  écrit par son nom, groupé par famille, avec la mesure qui le justifie :
  l'émulateur n'authentifie rien, donc IAM est refusé ; `ReadQuotas` n'est lu que
  par des sources de données, donc le refuser ne casse aucun `apply`.

### Corrigé

Défauts trouvés en auditant des packs entiers plutôt que des diffs, tous sur des
chemins qu'aucun client de conformance n'emprunte :

- **Détacher une IP ne faisait rien** (Scaleway). `PATCH /ips/{id}` avec
  `{"server": null}` était indiscernable d'une requête qui ne mentionne pas le
  champ, si bien que l'adresse restait attachée tandis que l'API répondait un
  succès.
- **Les volumes d'un serveur ne pouvaient être ni attachés ni détachés**
  (Scaleway), et un serveur supprimé ou terminé ne libérait pas ses adresses.
- **Deux créations concurrentes pouvaient recevoir la même adresse** (Outscale).
  Douze créations parallèles ont remis une adresse à deux machines ; avec un
  runtime, cela fait deux conteneurs configurés avec la même IP statique.
- **Un Subnet supprimé sous les machines qui y étaient placées** (Outscale), et
  avec un runtime cela démolissait le réseau sous-jacent sous des machines
  attachées.
- **Une création qui échouait laissait des machines en marche** (Outscale) :
  quatorze machines demandées dans un subnet qui en contenait onze répondaient une
  erreur et gardaient les onze, qu'aucun client ne suit.
- **`DryRun` était déclaré sur vingt actions et lu par aucune.** `CreateSubnet
  --dry-run` créait un pont sur l'hôte de l'opérateur et `DeleteVms --dry-run`
  détruisait la machine. Il est désormais répondu au point de montage, avant
  qu'aucun handler ne tourne.
- **Une paire de clés acceptait n'importe quoi**, y compris une valeur multi-ligne
  que cloud-init refuse plus tard — la machine démarrait avec les mauvais octets et
  refusait toute connexion.
- **`terraform destroy` échouait pour de bon sur un serveur avec
  `additional_volume_ids`** (Scaleway). Terminate ne détachait pas ses volumes,
  donc le disque continuait de nommer un serveur qui répondait 404 et chaque
  réessai heurtait « volume is still attached ». Le provider emprunte terminate,
  pas delete, et seul delete libérait quoi que ce soit.
- **Trois portes attachaient un volume et une seule demandait s'il était libre**
  (Scaleway) : une création ou une mise à jour nommant le volume racine d'un autre
  serveur le déplaçait, et les deux serveurs le listaient ensuite.
- **Créer un serveur prenait une adresse à une machine vivante** sans la retirer,
  si bien que sous un runtime deux machines revendiquaient la même adresse.
- **`scw instance ip delete <adresse>`** répondait un succès et gardait l'adresse.
- **`precondition failed:` s'affichait sans rien après les deux-points** : le jeton
  qu'émettait le pack n'était pas l'un des trois que le SDK rend.
- **`scw instance volume list name=vol`** revenait vide contre un volume appelé
  `myvolume` : le SDK documente ce filtre comme une sous-chaîne, avec cet exemple.

### Ajouté

- **`TestEveryCitedTestExists`** parcourt chaque commentaire du dépôt et échoue
  quand il cite un test qui n'existe pas. Trois audits d'affilée ont trouvé un
  correctif dont le commentaire nommait le test qui échouerait sans lui, alors que
  ce test n'avait jamais été écrit — y compris dans le commit qui invoquait la
  règle tout en l'enfreignant. Une règle écrite trois fois et enfreinte trois fois
  avait besoin d'un contrôle plutôt que d'une quatrième reformulation.
- **La surface upstream Scaleway est entièrement triée** : 0 opération laissée
  indécise sur instance, vpc, ipam, iam et marketplace.
- **La fixture de conformance parcourt les volumes et les adresses** : attacher,
  refuser la suppression sous un serveur, détacher, supprimer ; puis résoudre et
  supprimer une IP par son adresse ; et un apply/destroy Terraform sur
  `additional_volume_ids`. Chaque défaut trouvé par les audits vivait dans l'écart
  entre cette fixture et ce que le pack revendiquait. 68 routes sur 91 sont
  désormais prouvées par un vrai client, contre 64.

### Limites qui ont bougé

- `DryRun` est honoré sur chaque action Outscale servie, mais il ne **valide** pas :
  la réponse est émise avant que le handler ne tourne, donc un dry run d'une
  requête malformée répond quand même 200.
- `MaxVmsCount` est plafonné par création ; sans borne, une requête a alloué un
  million de ressources et tenté de démarrer un million de conteneurs à l'intérieur
  du handler.
- `SecurityGroupIds` est refusé plutôt que jeté en silence : dire à un client que
  ses règles ont été appliquées alors qu'aucune règle n'existe nulle part est la
  seule réponse pire qu'un 400.

## [0.2.0]

### Ajouté

- **`feint version --check`**, qui demande à GitHub si une version plus récente
  existe et affiche la commande pour l'installer, épinglée à cette version.
  Demandé plutôt que spontané : rien n'atteint le réseau tant que le drapeau n'est
  pas tapé, et `FEINT_NO_UPDATE_CHECK=1` le refuse d'emblée. Le binaire ne se met
  jamais à jour lui-même — dire vaut mieux que réécrire quelque chose que
  quelqu'un a vérifié.
- **Les fichiers de marque**, dans `docs/assets/brand/`, avec `docs/brand.md` pour
  ce qu'on peut en faire. Le logotype est en tracés plutôt qu'en texte, pour que le
  verrouillage rende pareil sur GitHub et dans une diapositive, et la section
  licence énonce ce que la licence Apache ne couvre pas : le nom et le logo.

## [0.1.0]

La première version publiée. Elle porte trois providers émulés sur un port, les
verbes de cycle de vie, et la machinerie qui maintient la surface émulée mesurée
contre les SDK des providers eux-mêmes plutôt que suivie à la main.

### Ajouté

- **Trois providers émulés sur un port.** Scaleway (Instance, VPC, IPAM, IAM,
  marketplace), Outscale (Vms, paires de clés, catalogue, Nets et Subnets) et
  Exoscale (instances, catalogue, clés SSH), servis par un binaire sans aucune
  dépendance Go externe.
- **Les commandes de cycle de vie** : `start`, `stop`, `restart`, `wait`, `status`,
  `logs`. Le binaire se met lui-même en arrière-plan — pas de Docker, pas de `&` —
  ce qu'aucun des émulateurs comparables ne sait faire.
- **`feint env <provider>`**, pour qu'`eval "$(feint env scaleway)"` suffise à
  pointer un vrai client sur l'émulateur. Les exports sur stdout, les mises en
  garde sur stderr.
- **`feint probe`** : chaque route montée pilotée depuis la description d'API de
  son provider, prouvant le protocole sans qu'un client soit installé. Cela ne
  compte jamais dans le score de conformance.
- **`feint docs`** : les tableaux de couverture du README, sa bannière de démarrage
  et le tableau de politique de contrat de `docs/limits.md` sont générés depuis les
  artefacts versionnés, et `--check` échoue quand ils dérivent.
- **La validation des réponses contre les documents OpenAPI des providers
  eux-mêmes**, avec la force de chaque contrat enregistrée plutôt que supposée.
- **De vraies machines derrière l'API** via Incus, avec un plan d'adressage qui est
  de l'arithmétique : masques bornés, containment et recouvrement imposés, comptes
  d'adresses calculés, et une VM placée sur un subnet portant l'adresse que l'API a
  publiée.
- **Des suites de conformance pilotant les vrais clients** : `scw`, `oapi-cli`,
  `exo`, Terraform et OpenTofu.
- **`feint doctor`** : les diagnostics d'hôte qui encodent les pièges que ce projet
  a déjà payés — le port, la version d'Incus qu'exigent les ACL, ce que `--vm auto`
  choisirait réellement ici, les clients dans le PATH, et le `ProxyJump` de
  `~/.ssh/config` qui fait paraître absent un `sshd` qui fonctionne.
- **`feint snapshot save/load/list/rm`**, sur deux nouvelles routes
  d'administration (`GET` et `PUT /_feint/state`). Un snapshot est exactement ce
  qu'écrit `serve --state`, pris en cours de run plutôt qu'à la sortie, pour qu'une
  fixture soit atteinte une fois et qu'on y revienne autant de fois qu'un test en a
  besoin. Charger remplace le store : une fixture ne doit pas dépendre de ce que la
  session a fait avant elle.
- **Une référence de routes générée** (`docs/routes.md`) et une **matrice de
  versions de clients générée** dans le README, toutes deux sous
  `feint docs --check`. La première est écrite par le binaire qui monte les
  routes ; la seconde est lue depuis le workflow qui installe les clients.

### Sécurité

- **`serve` se lie à `127.0.0.1` par défaut**, là où il se liait auparavant à
  toutes les interfaces. Cet émulateur accepte chaque credential sans en vérifier
  un seul et, avec `--vm`, démarre des conteneurs avec les privilèges de
  l'opérateur ; l'ancien défaut offrait cela à n'importe quel réseau sur lequel se
  trouvait la machine. `--addr` l'expose toujours pour qui le décide.
- **Les suites de conformance refusent de lancer un client qui n'est pas pointé sur
  un émulateur local** (`tools/conformance/guard.sh`). Chaque client officiel
  retombe sur des credentials stockés quand l'environnement ne dit rien : un
  environnement vide doit donc échouer bruyamment plutôt qu'atteindre un compte
  payant.
- **`PUT /_feint/state` borne le corps qu'il lit** (64 Mio). Il décodait tout ce qui
  arrivait, ce qui faisait d'une requête surdimensionnée un plantage — la classe de
  défaut que `SECURITY.md` déclare dans le périmètre.
- **Le DNS rebinding est refusé.** Se lier à la boucle locale arrête un réseau, pas
  un navigateur : une page que l'opérateur visite peut résoudre son propre nom vers
  `127.0.0.1` et piloter l'émulateur depuis là — lire et remplacer son état, et
  avec `--vm`, démarrer des conteneurs. Un audit a atteint `/_feint/health` avec
  `Host: evil.example` et obtenu un 200. Quand l'adresse d'écoute est une adresse
  de boucle locale, les requêtes dont le `Host` en nomme une autre sont désormais
  refusées ; `--addr` sur toute autre adresse désactive le contrôle, parce que
  cette exposition a été demandée.

### Corrigé

- **Le mode OVN ne pouvait créer aucun réseau sur un hôte frais.** Le pilote
  construisait son uplink sans déléguer aucune route, si bien qu'Incus refusait
  chaque réseau dont le bloc tombait hors du /24 de l'uplink lui-même — c'est-à-dire
  chaque bloc, puisqu'un client choisit son plan d'adressage en fonction de ce
  qu'il teste. Il délègue désormais chaque bloc au moment où le réseau est créé, un
  à la fois : déléguer une plage entière d'avance se transforme en vraies routes
  d'hôte et entre en collision avec ce qui y vit déjà. Invisible sur une machine
  dont l'uplink précédait le contrôle ; trouvé en installant sur une machine propre
  et en demandant un subnet.
- **Une suppression en course avec un démarrage pouvait ressusciter la machine.**
  Les réécritures après un appel au runtime passent par `store.Commit`, qui refuse
  de réinsérer une ressource supprimée entre-temps.
- **Une machine qui échouait à démarrer rapportait `running`.** Elle atteint
  désormais l'état d'échec propre au provider, tandis que le mode sans runtime
  documenté atteint toujours running.
- **Un numéro de page que personne n'envoie faisait paniquer chaque route de
  liste**, emportant la réponse avec lui.
- **Le `SubnetId` d'Outscale était accepté et jeté**, si bien qu'une Vm rapportait
  un succès et n'allait nulle part.
- **`stop_in_place` répondait `stopped`, un état qu'aucun client n'avait
  demandé.** Le SDK déclare `stopped in place` à côté de `stopped`, et le provider
  Terraform interroge en boucle pour celui exact que son `state = "standby"` a
  demandé : le plan échouait avec « expected state stopped in place but found
  stopped ». Ni `scw` ni la suite de conformance n'exerce standby, ce qui explique
  qu'un vrai provider l'ait trouvé en premier.
- **`per_page` était ignoré sur chaque route de liste d'`instance/v1`.** Ce produit
  épelle la taille de page `per_page` ; les plus récents (`vpc`, `ipam`) l'épellent
  `page_size`, et l'assistant partagé ne lisait que le second. Chaque liste
  d'instances servait cinquante éléments quoi que le client demande. Les deux
  orthographes sont lues désormais.
- **`ListServers` ignorait `state`, `tags` et `commercial_type`.** Un filtre jeté
  est pire qu'un filtre refusé : `state=running` renvoyait tous les serveurs
  arrêtés qui existaient, dans une réponse ayant exactement la forme de la bonne.
- **`UpdateServer` jetait `commercial_type` et `volumes`.** Retyper un serveur
  répondait 200, ne changeait rien, et laissait Terraform demander le même
  changement à chaque plan. Les deux sont appliqués désormais, avec la restriction
  que le SDK documente — un type ne peut pas changer pendant que le serveur tourne
  — et un volume nommé par un id qui n'existe pas est refusé plutôt qu'ignoré en
  silence.
- **Les champs qu'un client envoie et qu'aucun handler ne lit font désormais
  échouer le run de conformance.** L'émulateur les enregistrait à
  `/_feint/conformance` depuis toujours, et personne ne regardait : il listait
  `commercial_type` et `volumes` sous `unread_request_fields` run après run pendant
  que Terraform bouclait. Une introspection sur laquelle personne ne fait gate est
  un aveu que personne n'entend.

[0.2.0]: https://github.com/stephrobert/feint/releases/tag/v0.2.0
[0.1.0]: https://github.com/stephrobert/feint/releases/tag/v0.1.0
