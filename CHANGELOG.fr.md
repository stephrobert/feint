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

## [Unreleased]

### Ajouté

- **Les stacks d'exemple sont appliquées à de vraies machines chaque nuit, et
  la porte qui le fait dit combien de fois (#504).** `conformance:functional`
  est la seule chose ici qui applique `examples/stacks/` contre un runtime, et
  rien en CI ne la jouait : aucune jambe de `runtime-proof.yml` n'applique de
  stack, et `mise run conformance` saute les suites de stack sans runtime.
  C'est elle qui a fait apparaître la course détachement contre démarrage, au
  prix de trois pull requests (#577, #578), sur un défaut plus vieux que toute
  la plage bissectée. Elle tourne désormais dans un job à elle sur ce workflow.
  Un job plutôt qu'une étape, parce que les suites ssh de la jambe incus-ovn
  étaient rouges quatre des sept dernières nuits planifiées et que chacune
  laisse des jeux de règles et des réseaux que `feint stop` ne balaie pas : une
  étape y attendrait derrière un rouge, puis rencontrerait un portillon
  qu'elle ne pourrait pas passer. **Trois passes**, et le nombre est un
  arbitrage : cette classe de défaut a frappé 9 fois sur 13, donc une seule
  passe la déclare absente 31 % du temps, là où trois passes ramènent ce risque
  à 3 %, pour 295 s la passe.

### Corrigé

- **Le portillon de sortie de la porte des stacks pose enfin la question que
  son propre commentaire annonçait, et la porte cesse de choisir son runtime
  en silence (#504).** Elle se terminait sur
  `guard_leftovers_for "$RUNTIME" "the end of the run"`, sous un paragraphe
  affirmant que la question du portillon était reposée à la sortie. Le garde
  n'arme cette question que sur le littéral `doorstep` : une passe qui laissait
  une machine ou un réseau sortait donc en 0, et c'est l'exécution suivante qui
  rencontrait le refus. C'est l'état que #521 avait retiré de
  `mise run conformance`, décrit ici et jamais fait. Elle ouvrait par ailleurs
  sur `RUNTIME="${FEINT_FUNCTIONAL_RUNTIME:-incus-ovn}"`, donc un `FEINT_VM`
  exporté était ignoré sans un mot : la ligne de #574, un répertoire plus loin,
  sur la seule porte dont le sujet est justement l'assertion où les deux modes
  divergent. La résolution est passée dans `tools/runtime-mode.sh`, partagée
  avec `evidence:update`, dont les tests inchangés sont ce qui prouve que le
  déplacement n'a rien changé.

- **Les deux sorties nulles de la stack Exoscale ne viennent pas de cet
  émulateur (#520).** Le constat déduisait, sans l'instrumenter, que le GET par
  identifiant du pack courait contre sa propre complétion asynchrone. Mesuré le
  2026-08-28 contre le pack directement : 100 lectures par identifiant à la
  suite sur un répartiteur, 100 paires création puis lecture neuves, et 50 pour
  le pool d'instances. **Zéro** nom, adresse ou taille vide. Il n'y a pas de
  fenêtre : la création écrit `name` et `ip` dans le store *avant* d'émettre
  une opération qui annonce déjà `state: success`. La troisième porte par
  identifiant de la stack se relisait correctement dans le même apply, donc ce
  qui a produit les valeurs nulles distingue les trois, et ce pack ne les
  distingue pas. `docs/limits.md` porte la mesure et la réfutation. Ce qui
  lèverait la limite est le correctif amont
  `terraform-provider-exoscale#573` ; aucune modification de ce pack ne le
  ferait.

- **Un groupe de sécurité Exoscale s'arrête à l'interface publique, là où le
  vrai cloud l'arrête (#574).** Exoscale le dit en une phrase : « Security
  group rules do not apply to traffic inside private networks ». Depuis
  `a344f8d` (#494/#475), cet émulateur les appliquait quand même. Le groupe
  `default` émulé ne porte aucune règle d'entrée, donc son jeu de règles se
  traduit par un défaut `drop`, et le pilote l'écrivait sur la NIC de
  rattachement : **deux instances d'un même réseau privé ne se joignaient
  plus**. Mesuré le 2026-08-27 sous `--vm incus`, avec un réseau et deux
  instances créées en `--public-ip none`, dont la seule interface est celle du
  rattachement : `security.acls=exo-…` et
  `security.acls.default.ingress.action=drop` sur la NIC, 0/10 sondes
  connectées ; avec le correctif, plus aucune clé `security.acls`, 10/10. Sous
  `--vm incus-ovn`, le même jeu de règles fautif était posé et ne mordait pas,
  parce que le `allow` fourre-tout en sortie de l'émetteur, à la priorité 300,
  surclasse le refus par défaut de la NIC réceptrice à 100/111 (#491). Tout
  vert obtenu là reposait donc sur un ordre de règles, et l'état d'après se lit
  sur la NIC, jamais sur une connexion qui passe.

  Ce qu'un groupe couvre est désormais un fait provider porté par la couche
  partagée, et non une règle de cette couche : le pack le déclare par interface
  sur `machine.Attachment.Unfiltered`, `machine.GroupSync` le lit sur le plan
  que le pack déclare déjà, et il arrive au pilote via
  `FirewallBinding.Unfiltered`. Scaleway et Outscale ne déclarent rien et
  continuent de filtrer leurs NIC privées, ce que leurs propres suites réseau
  vérifient. Conséquence à connaître en lisant l'hôte : une instance Exoscale
  sans adresse publique, rattachée seulement à des réseaux privés, ne porte
  **aucun jeu de règles sur aucune interface**, faute d'interface qu'un groupe
  couvre.

- **Les trois instruments qui l'ont caché, réparés dans le même lot (#574).**
  `.github/workflows/runtime-proof.yml` jouait deux des trois suites réseau et
  pas `tools/conformance/exoscale/network.sh`, sur aucune de ses deux jambes :
  rien en CI ne la rejouait. `TestEveryNetworkSuiteIsReplayedByTheRuntimeProof`
  dérive maintenant la liste des suites présentes sur disque, au lieu d'une
  liste que quelqu'un doit se rappeler. `mise run evidence:update` écrasait en
  silence un `FEINT_VM` exporté sur sa jambe runtime
  (`FEINT_VM="${FEINT_EVIDENCE_VM:-incus}"`), ce qui a produit deux
  attributions fausses successives pendant le diagnostic ;
  `tools/evidence/mode.sh` honore désormais l'appelant, refuse par leur nom un
  désaccord entre les deux variables et le mode `off`, et **annonce le mode
  qu'il exécute** avant que quoi que ce soit démarre. Enfin, l'en-tête de la
  suite réseau Exoscale affirmait que le pack « ne synchronise pas encore ses
  groupes de sécurité sur les machines » : vrai avant `a344f8d`, lu comme un
  fait courant des mois après, et c'est lui qui détournait tous les lecteurs du
  pare-feu.

## [0.11.0] - 2026-08-26

La version où l'émulateur a cessé de se croire sur parole. Quatre défauts
trouvés à la main en une soirée — un groupe de sécurité servi et appliqué à
rien, une capacité de répartition que personne ne matérialisait, un répartiteur
qui ne distribuait rien, deux machines dotées d'une même adresse — étaient tous
les quatre verts : apply à 0, second plan vide, destruction propre, et trois
d'entre eux sans une seule ligne ERROR. Rien n'échouait, donc rien ne pouvait
les attraper sauf quelqu'un qui lisait l'hôte.

### Ajouté

- **`/_feint/health` répond désormais qui livre la répartition, et plus
  seulement si le runtime en serait capable : `enforced.balancing`,
  schema_version 6 (#481).** `capabilities.balancing: true` disait vrai du
  runtime (OVN distribue réellement) pendant que le répartiteur d'une stack
  Scaleway ne laissait aucune trace sur l'hôte, parce que ce pack ne confie
  rien au runtime ; les deux affirmations étaient vraies, et une suite branchée
  sur la capacité, ce que ce dépôt lui-même conseille, aurait affirmé une
  distribution que personne n'a promise. L'objet `enforced` gagne `balancing` à
  côté de `firewall` : la liste des packs qui confient leurs répartiteurs au
  runtime, aujourd'hui `["outscale"]`. Une suite qui veut affirmer la
  distribution se branche sur la conjonction des deux moitiés ;
  `tools/conformance/outscale/balancer.sh` le fait désormais, et se saute à
  voix haute quand une moitié manque. Les packs Scaleway et Exoscale sont
  volontairement hors de la liste : leurs répartiteurs enregistrent la
  configuration et ne transfèrent rien ([limits.md](docs/limits.md)), et une
  capacité non déclarée qui vaut absente est ce qui garde cette phrase honnête.

- **`feint.yaml`, et les deux verbes qui le lisent : `feint up` et `feint down`
  (#189, #190).** Les drapeaux qui décident de ce que l'émulateur d'un collègue
  sait faire — quel runtime, quel provider, quels contrats, quel état de départ
  — vivaient dans un historique de shell et dans un paragraphe de README, et un
  dépôt qui en exigeait un précis n'avait aucun moyen de le dire. Une
  déclaration à la racine le dit désormais, et `feint up` la lit : il contrôle
  que l'hôte peut livrer le runtime nommé et **refuse avant que rien ne
  démarre** plutôt que de dégrader, démarre l'émulateur avec l'état et les
  contrats déclarés, exporte l'environnement client du pack lui-même, lance le
  moteur dans le répertoire déclaré en laissant passer sa sortie telle quelle,
  attend chaque condition `ready:` à voix haute et avec une échéance, puis
  imprime les points d'entrée. `feint down` détruit puis arrête, en disant ce
  qu'il jette.

  Le schéma est fermé : **une clé qu'il ne nomme pas est refusée par son nom au
  chargement**, avec la liste de celles que ce bloc accepte, de sorte qu'un
  fichier mal tapé échoue au chargement et non au premier comportement
  surprenant. Ce que ce fichier ne porte délibérément pas est refusé avec sa
  raison plutôt qu'en clé inconnue — `emulator.coverage`, `emulator.shapes`,
  `emulator.expose_to_network` et `proxy` disent chacun où vit réellement ce que
  le lecteur cherchait. Et une clé que le schéma connaît et qu'aucun verbe ne lit
  encore est annoncée au chargement : un fichier qui accepte tout et n'applique
  qu'une partie est le mensonge exact que ce projet existe pour éviter.

  `up` compose le cycle de vie au lieu de le remplacer : il rend la déclaration
  dans les drapeaux que `feint start` accepte déjà, si bien que `status`, `logs`
  et `stop` connaissent l'instance qu'il a montée. La surface CLI passe en
  version 18 pour les deux verbes ; rien n'a été retiré, aucun code de sortie
  n'a bougé.

  Quatre pièges disparaissent, chacun payé à la main le 2026-08-24 en appliquant
  les trois stacks d'exemple : un module local qu'une copie des `*.tf` laisse
  derrière (le moteur tourne dans le répertoire déclaré, en place) ; une variable
  `endpoint` dont le défaut vise un port où rien n'écoute (`iac.vars`, avec
  `${feint.endpoint}` pour seule substitution) ; un endpoint dont le chemin `/v2`
  appartient à la valeur chez un provider et pas chez les autres (il vient de
  l'`Env` du pack, jamais d'un champ) ; et `FEINT_EXOSCALE_ALLOW_TERRAFORM`, lu
  côté serveur, donc inopérant s'il est exporté après le démarrage
  (`emulator.env`, posé avant le spawn).

  Éprouvé sous un vrai runtime de machines, et pas seulement en plan de
  contrôle : la même déclaration Scaleway avec `--runtime incus-ovn` a appliqué
  50 ressources, replanifié vide et détruit, et l'hôte portait ce que l'API
  décrivait — six conteneurs en marche, trois réseaux OVN sur les blocs que la
  stack déclare, trois jeux de règles marqués `feint security group` répartis sur
  six interfaces, une ACL d'isolation par réseau, et rien après `feint down`.
  `runtime.images` est contrôlé sur la station avant que rien ne démarre et
  n'est jamais construit, avec trois réponses plutôt que deux : présente,
  absente du jeu de préchauffage et refusée avec `feint images --only <nom>`, ou
  hors de ce jeu et annoncée comme un premier démarrage qui en dérivera une. Un
  runtime que l'hôte ne peut pas livrer est refusé avant tout démarrage, en
  nommant la moitié manquante et les trois portes qui restent.

  Chaque stack d'exemple porte sa déclaration, et
  `tools/conformance/environment/` pilote les deux verbes sur un dépôt fixture
  en `--vm off`. [docs/environment.md](docs/environment.md) décrit le chemin de
  `git clone` à un `terraform apply` qui passe, et sa référence de champs est
  générée depuis le schéma, sur le rail de `feint docs --check`.

### Modifié

- **Le bloc public Outscale émulé est un `/28`, plus un `/24`, et la suite de
  conformance a cessé de passer quatre minutes à libérer des adresses.** Ce que
  ce bloc doit tenir — l'allocation refuse au-delà de la dernière adresse avec
  un `9029` typé, une adresse libérée revient au pool — ne dépend pas de leur
  nombre, et le pic réellement détenu simultanément par l'ensemble des suites,
  fixtures et stacks d'exemple de ce dépôt est de **deux**. Épuiser un `/24`
  coûtait 254 appels `DeletePublicIp` à ~700 ms de démarrage de client chacun.
  Mesuré sur cette station : jambe `octl` **368 s → 142 s**, jambe `fields`
  **435 s → 209 s**, les deux toujours vertes avec le gate d'omission armé.

  La borne de l'allocateur se déduit désormais du préfixe au lieu d'être écrite
  une seconde fois, `ReadPublicIpRanges` publie cette même constante, et
  `TestTheAllocatorStopsWhereTheCatalogueSaysItDoes` parcourt la plage publiée
  jusqu'au bout puis exige que le refus tombe sur l'adresse suivante — éditer
  un seul des deux côtés le fait donc échouer. Falsifié trois fois : publier
  une autre plage, désolidariser la borne du préfixe, laisser le pool
  distribuer deux fois la même adresse ; les trois rougissent.

  Trouvé en chemin : `TestAnOutscaleBarrageLeavesTheStoreCoherent` prétendait en
  commentaire attraper un pool distribuant une adresse à deux appelants, et
  jetait l'adresse qu'on venait de lui donner (`_ = ip`). Il enregistre
  maintenant chaque adresse et vérifie les doublons, et traite l'épuisement pour
  ce qu'il est : la réponse attendue.


- **La suite Outscale pilote `octl`, et le client qu'elle pilotait est archivé**
  (#460). `outscale/oapi-cli` et `outscale/osc-cli` sont tous deux
  `archived: true` sur l'API GitHub, en lecture seule, avec « Deprecated
  Outscale CLI » dans leur propre description ; `outscale/octl` avait reçu un
  push le jour de la mesure. Une suite qui pilote un client archivé prouve
  l'émulateur contre quelque chose qu'Outscale ne livre plus : l'étoile polaire
  s'est déplacée avec.

  `tools/conformance/outscale/oapi-cli.sh` est devenu
  `tools/conformance/outscale/octl.sh`, la jambe de matrice `oapi-cli` est
  devenue `octl`, et tous les autres scripts Outscale qui pilotaient un client
  ont suivi : `faults.sh` (qui tourne sur la jambe `fields` et aurait échoué sur
  un binaire absent), `outscale/network.sh`, `outscale/balancer.sh`,
  `outscale/ssh.sh`, `parity.sh` et `tools/demo/osc.sh`.

  **La couverture a été mesurée avant et après, opération par opération, et rien
  n'a été perdu.** La ligne de base vient du `/_feint/conformance` de
  l'émulateur sur la suite intacte, comparée à la même lecture de la suite
  migrée : **77 opérations distinctes avant, 77 après, aucune gagnée, aucune
  perdue**, et les sept axes d'évidence identiques au bit près. Le compte de
  sites `prove_begin negative` vaut 13 avant et 13 après, `behaviour` 6 et 6 :
  aucune assertion ne peut avoir disparu dans la réécriture.

  Le total d'appels passe de 700 à 677, et les douze écarts ont tous la même
  cause : **`oapi-cli` envoyait trois requêtes pour chaque 409 rencontré**, avec
  une temporisation entre elles, là où `octl` en envoie une.
  `AcceptNetPeering` 7 → 3, `CreatePublicIp` 261 → 257, neuf opérations en
  baisse de deux exactement par site de refus. Deux opérations montent :
  `ReadNets` 2 → 4 pour le nouveau témoin `-o raw`, `ReadPublicIps` 4 → 5 parce
  que l'inventaire d'adresses est désormais lu trois fois.

  Trois faits sur `octl` portent le reste, et chacun est écrit dans la suite :
  **l'endpoint porte `/api/v1`** — l'inverse d'`oapi-cli`, parce qu'`octl` comme
  le provider Terraform ≥ 1.7 lisent `osc-sdk-go`, dont le gabarit d'endpoint par
  défaut est `%s://api.%s.outscale.com/api/v1` ; **`iaas api <Call>` et jamais un
  alias**, parce qu'`octl iaas net list` se résout en `octl iaas api ReadNets` et
  qu'un alias est une commodité du CLI là où c'est l'API qu'on mesure ; et
  **`-o raw` sur chaque appel**, parce que le `-o json` par défaut reforme la
  réponse et déballe `{"Nets":[…],"ResponseContext":{…}}` en liste nue. Une
  assertion en tête de suite est le témoin du troisième : elle échoue si la forme
  brute cesse de porter l'enveloppe, et elle échoue si `-o json` cesse de
  reformer.

  `feint env outscale --client octl` imprime la forme, et la note `oapi-cli` dit
  maintenant que le client est archivé et nomme son remplaçant vivant.
  `feint doctor` cherche `octl`, le rôle Ansible l'installe contre le fichier de
  sommes publié en amont (l'extraction FUSE de l'AppImage, son enrobage APPDIR et
  son empreinte épinglée à la main disparaissent avec), et le vocabulaire de
  clients de `feint proxy` gagne `octl` — user agent mesuré au travers du proxy :
  `octl/v0.0.31`, et rien d'autre.

  **Ce que ça coûte, dit plutôt qu'escamoté.** `octl` passe environ 700 ms à
  démarrer à chaque invocation, sans le moindre réseau, contre environ 30 ms pour
  la requête : la suite passe de 177 s à 369 s, et le goulot se déplace d'une
  temporisation sur onze appels à un coût fixe sur tous. Le bloc d'adresses
  publiques est désormais rempli par un seul processus `--waitfor` (255 appels en
  51 s, contre 186 s en processus séparés) plutôt qu'un processus par adresse.
  Trois arguments de filtre ne s'expriment pas du tout en drapeaux `octl` — un
  tableau de flottants ne produit aucun drapeau, et les deux filtres de date sont
  enregistrés en scalaires quand leur lecteur réclame une liste — donc ils
  voyagent en `--payload` ; `docs/limits.md` porte la mesure et dit pourquoi un
  payload mal formé ne peut pas passer en silence.

### Corrigé

- **Le répartiteur deux-tiers distribue vers les arrière-plans que le runtime
  sait prendre, retient les autres en les nommant, et écrit les deux moitiés là
  où un lecteur les rencontre (#483).** Le refus en bloc posé par #457 a été
  mesuré une seconde fois, et il coûtait plus qu'il ne protégeait : la stack
  d'exemple Outscale (un arrière-plan sur le subnet du répartiteur, un sur un
  autre, la forme ordinaire du tiers public) laissait l'hôte porter un
  répartiteur enregistré **sans arrière-plan ni port** pendant que l'API en
  décrivait deux sains ; apply exit 0, second plan vide, zéro ligne ERROR, un
  seul WARN pour toute trace, invisible pour toutes les portes. Le pilote
  scinde désormais au lieu de refuser en bloc : les arrière-plans du bloc du
  répartiteur sont distribués (`EnsureBalancer` rend une `BalancerDelivery`),
  ceux d'un autre bloc sont retenus avant l'écriture, jamais remis au démon
  pour mourir au milieu d'une mise à jour, et le pack consigne les deux listes
  dans le `Runtime` du répartiteur (`balancer-distributed`,
  `balancer-undistributed`, lisibles par `/_feint/state`), tenues à jour à
  chaque register, unlink et changement de listener. Le WARN reste un WARN,
  parce que la limite est permanente sur une configuration valide, mais l'état
  porte désormais le fait au lieu que le journal le porte seul. Ce qu'il
  faudrait pour une livraison entière est nommé dans
  [limits.md](docs/limits.md) : qu'Incus lève sa restriction au sous-réseau
  sur les cibles de `network load-balancer`, ou un seul réseau runtime par
  Net ; d'ici là, cette scission est toute la vérité que l'hôte sait porter.

- **Un répartiteur de charge devant des machines d'un autre sous-réseau cesse de
  revendiquer un plan de données qu'il n'a pas, et l'ordre Terraform ordinaire
  cesse d'échouer** (#457). Le rejeu des quinze stacks tierces de
  `examples/stacks/surveyed.md` sous `--vm incus-ovn` l'a trouvé, sur le
  `two-tier-architecture` de `chimere-eu/ztiac` : un balanceur public devant des
  machines privées est la forme ordinaire, et **les deux** balanceurs de cette
  stack tombaient dessus. L'`apply` réussissait sur 54 ressources alors que
  l'hôte ne portait aucun balanceur, et seul le journal de l'émulateur le
  savait — la même famille que #454.

  **La cause était un garde posé sur une extrémité seulement.** `EnsureBalancer`
  contrôlait l'adresse d'*écoute* contre le bloc du réseau (le garde de #315) et
  se contentait de `netip.ParseAddr` sur chaque *cible*, si bien qu'un backend
  sur un autre sous-réseau passait tous les refus et mourait dans le runtime, au
  milieu de l'écriture, laissant le balanceur debout sur les backends qu'il
  avait déjà.

  **La question de savoir si OVN pouvait servir cette forme a été mesurée avant
  d'être tranchée**, sur Incus 7.2 avec OVN le 2026-08-25, sur deux réseaux
  créés par l'émulateur. Un backend hors du réseau du balanceur est refusé,
  `Target address is not within the network subnet` ; appairer les deux réseaux
  — les deux moitiés `CREATED` — ne relâche rien, le refus est le même mot pour
  mot ; poser le balanceur sur le réseau des *cibles*, ce qui est le placement
  qui servirait la forme, est refusé à l'autre bout, `Load balancer listen
  address "10.181.0.5/32" overlaps with another network or NIC` ; et il n'existe
  aucune clé pour déclarer l'adresse, un réseau OVN répondant `Invalid option
  for network ... option "ipv4.routes"`, parce que seule une NIC porte
  `ipv4.routes.external` et qu'une VIP n'a pas de NIC. Le seul placement que le
  runtime accepte réclame une adresse d'écoute n'appartenant à aucun bloc émulé
  — exactement la classe d'adresses que #315 a mesurée devenant muette en trois
  minutes, et ce n'est pas non plus l'adresse que l'API a publiée.

  **C'est donc la limite qui bouge, pas le plan de données.** Le pilote refuse la
  forme par son nom, entière, avant toute écriture ; le pack la rapporte en WARN
  en nommant ce que l'API continue de décrire, parce qu'une limite qui tient
  toute la vie d'une stack rapportée en ERREUR est la façon dont un journal
  apprend à ignorer ses erreurs ; et `capabilities.balancing` énonce désormais
  ses deux bornes — l'adresse du balanceur *et* ses backends sur son propre
  réseau. `docs/limits.md` porte les quatre mesures. Le `200` reste, comme celui
  du vrai cloud.

  **Et l'ordre Terraform ordinaire cesse d'échouer.** Un balanceur créé avant ses
  machines était écrit avec un port ne nommant aucun backend, ce que le runtime
  refuse (`Missing VIP target(s)`), avant de se réparer au register suivant. Le
  même corps **sans port** est accepté — mesuré, avec la vidange : un `PUT` ne
  portant ni backend ni port arrête la distribution, donc un balanceur qui perd
  sa dernière machine cesse réellement de recevoir. L'erreur est supprimée
  plutôt que journalisée plus discrètement.

  **Une limite bouge aussi sur `/_feint/health`** : `capabilities.balancing` peut
  désormais passer à faux en cours d'exécution, selon la règle que #454 a écrite
  pour le pare-feu — à sens unique, et seul un refus *de l'hôte* compte, jamais
  un refus que ce pilote a prononcé lui-même.

  Éprouvé contre le vrai runtime, en lisant `incus network load-balancer
  list`/`show` et non la réponse de l'API : le balanceur apparaît sur l'hôte dès
  la création avec 0 port, le register inter-sous-réseaux le laisse intact avec
  un WARN et zéro ERREUR sur toute l'exécution, et — le contrôle positif qui
  donne un sens à ce vide — un balanceur du même sous-réseau porte bien son
  backend et son port. Falsifié quatre fois de plus
  (`tools/falsify/specs/balancer-dataplane.json`, quatorze mutations au total) :
  garde de cible désarmé, ports rétablis sur un balanceur sans backend, retrait
  de capacité désarmé, et limite ramenée au niveau ERREUR.

- **Un groupe de sécurité portant une règle ICMP dont la source est en IPv6
  garde son pare-feu, et un jeu de règles que l'hôte refuse cesse de se lire
  comme un succès** (#454). Le rejeu des quinze stacks tierces de
  `examples/stacks/surveyed.md` sous `--vm incus-ovn` l'a trouvé ;
  `sergelogvinov/terraform-talos` livre
  `whitelist_admins = ["0.0.0.0/0", "::/0"]`, donc une liste blanche
  d'administration en double pile est ce qu'une stack apporte, pas un cas
  exotique.

  **Mesuré sur cette station, Incus 7.2 avec OVN, sur le témoin réduit de la
  campagne : deux groupes identiques à une règle près.** Avant, le groupe A
  décrivait une règle et en appliquait une ; le groupe B en décrivait deux et
  en appliquait **une**, sa règle ICMP purement absente, avec trois refus
  `Cannot use IPv6 source addresses with "icmp4" protocol` dans le journal de
  l'émulateur et `capabilities.firewall: true` publié pendant tout ce temps.
  Pire que le compte : la machine portant le groupe B ne portait **aucun jeu de
  règles** sur son interface, parce que le pack retourne avant d'attacher quand
  l'écriture échoue, si bien qu'un groupe en refus par défaut laissait sa
  machine sans filtre. Après, A fait 1/1 et B fait 2/2, la seconde règle écrite
  `protocol: icmp6, source: ::/0`, les deux machines portant leur jeu de règles,
  et aucun échec dans le journal.

  **La cause tient en une ligne, et dans l'écriture tout ou rien à côté
  d'elle.** `toACLRule` choisissait le protocole de l'ACL sur le seul nom de la
  règle (`case "icmp", "icmp4"`) sans jamais lire la famille d'adresses, tandis
  qu'`EnsureFirewall` écrit tout le jeu en un seul PUT, délibérément, pour
  qu'une règle révoquée disparaisse. Une règle inexprimable coûtait donc toutes
  les règles de son groupe, et l'API continuait de les décrire toutes : le
  commentaire de la fonction promettait l'inverse (« reports false for a rule
  the runtime cannot express, which the caller drops rather than
  approximating »), ce qui est le motif « un commentaire n'est pas un contrôle »
  de ce dépôt, dans la couche qui agit sur la machine de l'opérateur.

  La famille vient désormais des adresses de la règle : une source ou une
  destination IPv6 donne `icmp6`, une IPv4 donne `icmp4`, et les orthographes
  `icmp6`, `icmpv6` et `ipv6-icmp` sont comprises. `icmp` et `icmp4` restent
  tous deux agnostiques, volontairement : `icmp4` est le nom de fil du runtime,
  pas une revendication d'un pack, et la seule valeur de Scaleway est `ICMP`.
  Une règle qui ne fixe aucune famille devient donc **deux** règles, une par
  famille, parce que c'est ce que « ICMP depuis n'importe où » veut dire. Une
  règle qu'aucun protocole n'exprime (deux familles dans une règle, un nom qui
  contredit ses adresses, une adresse illisible) est laissée tomber **seule** et
  rapportée en WARN, en nommant le jeu et la règle, ce que « visiblement
  absente » était censé signifier.

  **Et une limite a bougé, observable sur `/_feint/health` :
  `capabilities.firewall` peut désormais passer à faux en cours d'exécution.**
  Un jeu de règles que l'hôte refuse, c'est l'hôte qui répond que ce processus
  n'applique pas ce que son API décrit ; la revendication est donc retirée, et
  dite dans le journal. Le retrait est à sens unique, et seul un refus **de
  l'hôte** compte : un nom que ce pilote refuse lui-même n'a jamais atteint le
  démon et provient d'un instantané restaurable, ce qui donnerait sinon à un
  fichier d'état fabriqué un interrupteur sur une revendication publiée.

  Falsifié de neuf façons (`tools/falsify/specs/icmp-family.json`), dont le
  choix sur le nom seul rétabli, le jumelage des deux familles retiré, le
  retrait désarmé, et le refus de la garde interne compté comme celui de
  l'hôte ; les neuf passent au rouge. Le témoin unitaire pilote `EnsureFirewall`
  par le `runner` injectable contre un faux démon qui reproduit le refus
  d'Incus, de sorte que le test mesure si l'écriture est *acceptée* plutôt que
  la façon dont elle est orthographiée.

- **Un Net à trois subnets ou plus s'appaire sous OVN, et un hôte qui porte déjà
  l'état qui l'en empêchait est réconcilié** (#456). Le rejeu des quinze stacks
  tierces sous `--vm incus-ovn` a coûté au registre son résultat vedette :
  `chimere-eu/ztiac` appliquait 80 de ses 95 ressources, en réclamait 15 au
  second plan, et son `destroy` échouait. Douze lignes de
  `Failed creating peer: More than one matching network peer was found` dans un
  seul apply, nommant six subnets, et l'émulateur continuait en ERROR : l'API
  décrivait donc un Net dont les subnets se routent entre eux alors que le
  runtime ne portait aucun peering entre eux.

  **La cause est en amont, et ce ne sont pas deux déclarations de la même
  paire.** Incus consomme un peering en cherchant une moitié *pendante* qui vise
  le réseau sur lequel la création atterrit, et cette recherche ne filtre que sur
  le réseau cible : aucune clause ne dit quel réseau porte la ligne (v7.2.0,
  `driver_ovn.go`, `PeerCreate`). Deux moitiés pendantes visant un même réseau
  font donc échouer **toute** création sur lui, quelles que soient les paires
  auxquelles elles appartiennent. Mesuré sur la station avec trois vrais réseaux
  OVN et rien de concurrent : `peer create A B B` rend pendant,
  `peer create C B B` rend pendant, `peer create B A A` rend l'erreur ci-dessus.
  `(A,B)` et `(C,B)` sont deux paires différentes, ce qui explique qu'un verrou à
  clé de paire n'aurait rien fermé, et qu'il faille trois subnets à un Net pour
  déclencher le défaut là où les fixtures à deux subnets ne l'ont jamais fait. Le
  pilote exclut désormais les deux **bouts** d'une paire, un verrou par réseau
  pris dans l'ordre trié : deux subnets d'un même Net ne peuvent plus déclarer en
  même temps deux moitiés visant le même réseau, et deux paires sans réseau
  commun continuent de s'exécuter en parallèle, ce qu'un verrou global aurait
  coûté.

  **Une seconde règle du runtime a été mesurée au passage, et elle est corrigée
  avec.** Supprimer un peer efface la cible de toutes les lignes qui visent le
  réseau sur lequel la suppression s'exécute, quelle que soit leur paire : sur
  une maille de trois réseaux, `peer delete B C` a laissé **A** tenant
  `{"name":"B","target_network":null,"status":"Errored"}`, et redéclarer cette
  moitié rend `A peer for that name already exists`, que ce pilote tolérait comme
  un succès. Le peering était donc rapporté appliqué et n'existait pas. Une paire
  dont le runtime n'appelle pas les deux moitiés `Created` est maintenant
  reconstruite des deux côtés, et toute suppression sur ce chemin demande
  l'étiquette écrite par `EnsureNetwork` avant de toucher un réseau, jamais le
  préfixe `fnt-` que n'importe qui peut taper.

  **Et « More than one matching network peer was found » est réconcilié plutôt
  que rapporté** : les moitiés pendantes qui visent ce réseau sont retirées, sur
  les seuls réseaux portant l'étiquette de cet émulateur, puis la création est
  réémise une fois, parce qu'un hôte laissé dans cet état par un run interrompu
  n'est réparé par rien de ce qu'un utilisateur peut taper.

  Mesuré de bout en bout contre Incus 7.2 avec OVN, le 2026-08-25 : un Net dont
  les trois subnets sont créés en parallèle donne trois réseaux OVN, six lignes
  de peering, six `Created`, et aucun échec dans le journal ; un hôte cassé à la
  main dans exactement l'état ci-dessus puis doté d'un quatrième subnet est
  revenu à **12 lignes sur 12 en `Created`**, puis 20 sur 20 et 30 sur 30 à
  mesure que deux subnets s'ajoutaient ; les six subnets et le Net se sont
  ensuite supprimés sans aucun 409, ne laissant aucun réseau et une table
  `networks_peers` vide. `tools/falsify/specs/peering-pairs.json` prouve que les
  gardes mordent (quatre mutations, quatre tests rouges), et `docs/limits.md`
  porte les deux règles du runtime avec les commandes qui les ont produites, y
  compris le cas qui n'est toujours pas garanti en une seule passe.

- **Un balayage ne piège plus l'hôte qu'il nettoie, et `feint clean --force`
  libère un hôte déjà piégé** (#455). Quinze stacks tierces rejouées sous
  `--vm incus-ovn` ont laissé deux réseaux OVN, deux jeux de règles et l'uplink
  sur une station où **aucune commande `incus` ne pouvait en retirer un seul**,
  et le fossoyeur était le balayage, pas le run suivant.

  La cause amont est le schéma d'Incus lui-même : des trois références que porte
  `networks_peers`, seule `target_network_id` n'a pas de cascade. Supprimer la
  cible d'un peering laisse donc la ligne derrière, tenant un identifiant qui ne
  résout plus rien. Toute opération sur le réseau survivant échoue alors sur
  `Failed loading target network: Network not found`, y compris la suppression
  de peering qui la réparerait. `incus network peer edit` rend 0 et ne persiste
  rien ; la 7.3 n'y change rien. Reproduit sur cette station, refus par refus,
  dans `docs/limits.md`.

  Trois comportements de cet émulateur transformaient cela en état permanent, et
  les trois sont désormais prévenus plutôt que réparés : `Prune` retirait les
  `ipv4.routes` de `feint-uplink` au passage, parce que l'uplink porte la même
  étiquette que le reste, et toute voie de gestion des réseaux qui en dépendent
  échouait ensuite à la validation ; rien ne détachait `security.acls` avant de
  supprimer un réseau que le jeu de règles retient et qui retient le jeu de
  règles ; rien ne supprimait la moitié qu'un peering laisse sur sa **cible
  survivante**, ce qui fabrique l'identifiant mort. L'uplink quitte maintenant
  le chemin ordinaire et part en dernier, routes intactes ; un jeu de règles est
  détaché avant la suppression et remis en place si la suppression refuse
  toujours ; les deux moitiés de chaque peering partent avant le réseau.

  Pour une station qui porte déjà l'état, **`feint clean --force`** retire la
  ligne par `incus admin sql`, le mécanisme supporté d'Incus et non une édition
  dans le dos du démon, après l'avoir imprimée entière pour qu'elle soit
  réinsérable. Elle n'est retirée **que si le réseau auquel elle appartient
  porte l'étiquette que cet émulateur a lui-même posée** : la ligne pendante
  d'un tiers est la même table, la même forme et la même cible morte, et un
  `--force` capable de l'atteindre serait un défaut pire que celui qu'il répare.
  `TestForceLeavesAThirdPartysDanglingPeerAlone` en est le témoin, et
  `tools/falsify/specs/trapped-station.json` prouve que les gardes mordent :
  quinze mutations, toutes rouges. La paire a aussi été jouée une fois contre
  le vrai runtime, où la ligne plantée sur un pont nommé `fnt-lab` était encore
  là ensuite.

- **`feint clean --check` rapporte les états qu'aucun balayage ne peut quitter,
  et sort en 1 dessus** (#455). Il répondait 0 en silence pendant toute la durée
  du blocage ci-dessus, parce que sans `--doorstep` il n'interrogeait que les
  services DHCP orphelins. Il nomme désormais une ligne de peering dont la cible
  ne résout plus, un réseau de l'émulateur dont l'uplink ne porte plus le bloc,
  et un jeu de règles attaché à un réseau déjà piégé par l'un des deux.

  Ce troisième état n'est délibérément pas la forme nue « un jeu de règles est
  attaché à un réseau » : `IsolateNetwork` en attache un à chaque réseau OVN
  ayant un voisin à tenir dehors, donc la forme nue refuserait tout run sain.
  C'est ainsi que le pas de la porte de #426 s'était mis à se déclencher sur des
  hôtes où rien n'allait échouer, et c'est ainsi qu'un contrôle finit désarmé.

### Ajouté

- **La stack Exoscale a une suite qui la pilote**
  (`mise run conformance:exoscale-terraform`, `tools/conformance/exoscale/terraform.sh`).
  Elle applique `examples/stacks/exoscale` par le fork corrigé épinglé dans
  `docs/limits.md`, exige un second plan vide et une destruction propre, et
  **refuse sur le pas de la porte** quand le fork n'est pas construit, en
  imprimant le remède entier, du clone à la compilation, pour qu'un lecteur n'ait
  jamais à ouvrir la documentation pour passer cette ligne.

  **Elle est hors de `mise run conformance` délibérément**, aux mêmes conditions
  que `conformance:ssh` : aucune porte de ce dépôt ne clone un dépôt tiers, ce
  qui mettrait la disponibilité de quelqu'un d'autre dans ce pipeline, et un
  client que ce projet a patché n'est pas le client officiel, donc il ne pourrait
  pas compter pour la conformance. Jusqu'ici la procédure n'existait qu'en prose
  dans `main.tf` et sous forme d'une exécution à la main notée dans
  `docs/clients.md`.

  Lancée aujourd'hui, elle échoue sur une cause nommée plutôt que sur du
  folklore : le fork corrige le client v2 et la stack utilise désormais des
  ressources v3 (#448).

- **Tout identifiant qu'une réponse Outscale publie désigne désormais un objet
  qu'une lecture retrouve** (#389, #383, #378). Une seule chaîne plutôt que
  trois rustines : le catalogue d'images est adossé à des snapshots que
  `ReadSnapshots` sert réellement, et `CreateVms` taille à chaque machine un
  volume BSU racine dans le snapshot que son image nomme.

  Ce qu'un client voit changer :

  - `ReadImages` répond `BlockDeviceMappings` sur chaque image du catalogue —
    `/dev/sda1`, sa taille, son type, `DeleteOnVmDeletion` et le `SnapshotId`
    dont elle est issue — là où la liste était vide. Une stack qui dimensionne
    son disque racine depuis l'image ne lisait rien.
  - `ReadSnapshots` répond les trois snapshots derrière le catalogue, et un
    volume peut être taillé dans l'un d'eux.
  - `CreateVms`, `ReadVms` et `UpdateVm` répondent le périphérique racine de la
    machine, en nommant un volume que `ReadVolumes` sert. C'est par là qu'une
    stack trouve le disque qu'elle ne doit pas supprimer, et c'était une liste
    vide.
  - `DeleteVms` détruit ce volume et libère ceux que le client a liés, chacun
    selon son propre `DeleteOnVmDeletion` — que `ReadVolumes` publie et filtre
    désormais par volume au lieu de la constante `false`.
  - `CreateImage` refuse un `Iops` sur un device mapping au lieu d'en stocker un.

  **Une limite a bougé** : une machine Outscale ne possédait ici aucun disque,
  elle en possède un. « Rien laissé derrière » change de sens d'autant pour
  chaque suite : le volume part avec la machine.

  La mesure qui renverse une prémisse : `Iops` valait 100 par défaut au motif
  que « le vrai cloud écrit Iops sur chaque Bsu d'image qu'il rend ». Sur les
  399 device mappings que le compte enregistré répond, **396 ne portent aucune
  clé `Iops`**, et les 3 qui en portent une sont les 3 d'un type de volume à
  IOPS provisionnées. `shapes/outscale.json` est l'union de tout ce qui a été
  observé, pas une exigence par élément. Le champ est décliné avec cette mesure
  à côté, et les quatre exemptions de `corpus/accepted.json` que cette chaîne
  périme sont supprimées.

- **L'axe `shape` était saturé à son propre plafond, et 619 échanges enregistrés
  ne nourrissaient rien** (#407). `shape` lisait 52 opérations sur 370, et son
  plafond valait **52 lui aussi**, à l'unité près par
  cloud : exoscale 14, outscale 23, scaleway 15 ; les chiffres publiés vivent
  dans le tableau généré de docs/routes.md. Il résolvait la couverture en
  parcourant `upstream.Reads`, une liste tenue à la main d'une soixantaine
  d'appels : aucun enregistrement ne pouvait donc le déplacer. Il nommait
  pourtant 292 opérations « aucune réponse du vrai cloud n'a été gardée », dont
  chacun des 619 échanges que ce dépôt détient déjà dans `corpus/`, qu'il n'a
  jamais consultés. Un contrôle dont le numérateur ne peut pas bouger n'est pas
  une mesure, et personne n'avait lu celui-ci depuis son écriture.

  Il parcourt désormais tout le catalogue, exactement comme
  `observedFieldsByOperation` dix lignes plus bas le faisait déjà, et
  `mise run shapes:fold` verse chaque corpus commité dans `shapes/`, hors ligne,
  sans compte. **`shape` passe de 52 à 134 sur 370**, et les 52 d'avant y sont
  toutes : c'est le même ensemble plus 82, pas un nombre neuf. 80 des 292
  enregistrements réclamés étaient déjà payés ; la file qui reste en compte 212,
  et `feint coverage --evidence coverage/evidence.json --gaps --axis shape` la
  nomme.

  **Une rédaction efface un type, elle n'en rapporte pas un**, et verser sans
  garde l'aurait écrit dans l'artefact dont tout le contenu est fait de types :
  l'enregistreur remplace une valeur par une chaîne, si bien que
  `osc/Client.ReadKeypairs.Keypairs` revenait en `array|string` et
  `LoadBalancers[].SecuredCookies` en `bool|string`, par-dessus des types qu'une
  lecture directe `--record` avait justes. Vingt-trois couples (opération,
  champ) des corpus portent un remplaçant, sept d'entre eux sur autre chose
  qu'une chaîne. `shape.IsRedacted` les refuse, et un chemin réécrit par
  l'assainisseur ne sert plus de clé du tout.

  `shapes/` n'est **pas** dérivé de `corpus/`, et c'est la mesure qui tranche :
  13 opérations sont enregistrées dans `shapes/` et dans aucun corpus, dont six
  qu'aucun pack ne sert, c'est-à-dire le versant « apprentissage » de la liste
  de lectures, une forme connue avant que le handler existe. Dériver les
  supprimerait. Le versement va dans un seul sens.

- **`feint evidence --reshape`** (#407) recalcule le seul axe `shape` d'un
  registre existant, hors ligne, depuis les catalogues sur disque. `shape` est
  le seul axe qui ne soit pas une propriété d'une exécution : `evidence` jette
  déjà ce que le serveur a répondu et le redérive. Un catalogue qui a grossi
  hors ligne ne coûte donc plus deux jambes de conformance, dont une sur un hôte
  capable de démarrer des machines, pour être publié. La commande refuse un
  registre dont les contrats ou les suites ont bougé depuis son écriture, et
  recalcule la colonne en entier : un catalogue qui a perdu une preuve fait
  baisser le chiffre au lieu de laisser une laisse de haute mer.

- **Un score dit où l'on en est ; une file dit quoi faire ensuite** (#408).
  `feint coverage --evidence <record> --gaps` liste, par cloud et par axe, les
  opérations à zéro **et le travail que chaque zéro nomme**. C'est cette seconde
  moitié qui compte : un zéro ne veut pas dire une seule chose. Une opération
  sans `shape` parce qu'aucun client ne l'atteint est un travail de suite de
  conformance ; le même zéro sur une opération qu'un client pilote à chaque
  exécution est une session d'enregistrement contre un vrai compte ; et un axe
  dont le registre dit que l'opération a *violé* son propre contrat est un
  défaut du pack. Trois zéros, trois personnes différentes — une file qui les
  confond tend la même liste de 158 noms aux trois.

  Quatre natures, chacune dérivée du registre et jamais d'un nom, d'une
  supposition ou d'une liste tenue à la main : `violating`, `unrecorded`,
  `undriven`, et `unproven` pour ce que le registre n'explique pas. La dernière
  est nommée plutôt que fondue dans une voisine, parce qu'un seau qui absorbe
  l'inexpliqué est la façon dont une file se met à mentir. Le vocabulaire voyage
  dans `--format json` : un consommateur n'a jamais à ouvrir le code pour savoir
  ce qu'une nature signifie.

  Ordonnée par le travail, puis par nom, et l'ordre est déclaré plutôt que
  calculé : le défaut d'abord parce que c'est le seul ici, puis
  l'enregistrement qui est à une session de la preuve, puis la suite qui est en
  amont de la plupart des axes. **Aucun pourcentage cible** — une file se
  travaille, elle ne s'atteint pas.

  Mesuré sur le registre commité le jour de la livraison : Exoscale demande 111
  suites de conformance, Scaleway 151 enregistrements. Deux métiers différents,
  que le score seul ne pouvait pas dire.

  `tools/falsify/specs/evidence-gaps.json`, six mutations, toutes rouges. L'une
  est restée verte au premier essai et a nommé une faiblesse de la fixture, pas
  du code : aucune de ses opérations n'était orpheline de pack, donc la garde
  qui écarte une telle opération pouvait être retirée sans qu'une assertion
  bronche.

- **L'axe de preuve `negative`, remesuré : de 35 sur 370 à 173 sur 370** (#390),
  et ce chiffre est celui qui sort, pas celui que quelqu'un visait. Par
  fournisseur : Scaleway de 18 à 97 sur 173, Outscale de 11 à 66 sur 93, Exoscale
  de 6 à 10 sur 104. 138 opérations le gagnent, aucune ne le perd, et 197 restent
  à zéro, de façon visible : une opération dont personne n'a pu enregistrer le
  refus n'est pas comptée. Deux passes identiques rendent le même 173, propriété
  dont ce chiffre avait besoin et que `behaviour` n'a pas (#398).

- **Le chemin malheureux est enregistré, et l'axe de preuve `negative` bouge pour
  la première fois par une mesure et non par une affirmation** (#390). Trois
  corpus de refus qu'un vrai cloud a réellement rendus
  (`corpus/scaleway/scw-refusals.jsonl`, `corpus/outscale/oapi-cli-refusals.jsonl`,
  `corpus/exoscale/exo-refusals.jsonl`), enregistrés depuis des comptes nommés le
  2026-08-21 avec `feint proxy`, assainis, commités, rejoués par
  `feint corpus --check` à chaque pull request, et réémis contre l'émulateur de
  la passe par `tools/conformance/refusals.sh`.

  **Aucune faute injectée n'y figure, et c'est cette borne qui donne sa valeur au
  chiffre.** `PUT /_feint/faults` sait faire répondre 403 à n'importe quelle
  opération, et #26 a fait en sorte qu'une telle réponse ne gagne rien : l'axe ne
  monte qu'en observant des refus qu'un client a réellement rencontrés. La
  définition vit désormais là où l'axe est calculé
  (`internal/core/emulator/assert.go`) : ce qui compte comme refus **demandé**, et
  pourquoi un refus injecté n'en sera jamais un.

  Une opération dont personne n'a pu enregistrer le refus reste à zéro, de façon
  visible, et trois familles sont nommées dans `corpus/README.md` avec leur
  raison : rien n'a été créé, donc ni 409 de nom déjà pris, ni 409 de dépendance,
  ni refus de quota n'entrent ici.

- **`feint replay --refusals-only`** : le drapeau lit l'enregistrement en entier
  et n'envoie rien tant que chaque échange n'est pas un 4xx. C'est ce qui permet
  de rejouer un corpus de refus à côté des autres suites d'une passe, contre
  l'unique émulateur qu'elles partagent, au lieu de lui en donner un à lui seul :
  un 4xx ne modifie rien, et cela se lit maintenant sur le fichier au lieu d'être
  promis à son sujet. Surface CLI version 12.

- **Un contrat peut désormais porter un champ que la description d'API du
  fournisseur ne déclare pas, et seulement avec l'enregistrement qui le prouve**
  (#370, #371). `tools/contract/extract-openapi.py --recorded-fields` replie un
  tel champ dans le schéma auquel il appartient, depuis un fragment YAML
  versionné, et recopie la citation (fichier de corpus, opération, chemin, date,
  raison) dans `contracts/<fournisseur>.json`, sous `recordedFields`. Ce
  mécanisme existe parce qu'une description d'API publiée peut être en retard
  sur l'API qu'elle décrit, et celle d'Exoscale l'est.

  **Ce qui compte, ce n'est pas la porte, c'est la serrure.** Un contrat est
  sinon extrait, puis ré-extrait par un crochet pre-commit : il ne s'édite pas à
  la main, et une porte ouverte sur « ce qu'une réponse a le droit de contenir »
  est exactement ce que la règle 4 interdit de laisser sans serrure.
  L'extraction refuse un schéma que ses documents ne définissent pas, et refuse
  une propriété que le document déclare déjà : quand l'amont rattrape son
  retard, l'entrée disparaît au lieu de laisser une citation que personne ne
  relira. Ensuite, `TestEveryRecordedFieldIsStillOnTheWire` rejoue à chaque
  exécution l'enregistrement cité et fait échouer l'entrée dont le chemin n'y
  est plus. C'est la règle de péremption de `corpus/accepted.json`, retournée :
  une exemption qui n'excuse rien est morte, une citation qui ne cite rien
  aussi.

- **Le corpus commité est maintenant rejoué contre le cloud, et attrape la
  dérive qu'aucun scan de SDK ne voit** (#359). `feint corpus --against-cloud
  --file <corpus> --endpoint <url>` réémet un enregistrement commité chez le
  fournisseur qui l'a produit. Même artefact, même comparateur, conclusion
  inverse : `corpus --check` le rejoue contre un émulateur neuf et une divergence
  veut dire *l'émulateur a tort* ; celui-ci le rejoue chez le fournisseur et une
  divergence veut dire *le cloud a changé*. Il n'y a ni second enregistreur ni
  seconde comparaison à tenir en phase — `internal/replay` est appelé par les
  deux, et ce que la seconde direction ajoute est seulement ce qu'un vrai compte
  exige.

  **C'est précisément ce que `internal/drift` ne peut pas voir.** Le scan de
  surface lit les SDK générés des fournisseurs et rapporte exactement une
  opération apparue ou disparue, parce que ces SDK sont générés depuis leur IDL.
  Il voit qu'une méthode existe ; il ne voit rien de ce qu'elle répond. Un statut
  qui passe de 200 à 201, un champ qui apparaît ou disparaît, une liste qui
  change d'ordre, un refus qui n'en est plus un : la signature Go est identique
  avant et après, la baseline reste verte, et #270 a trouvé trois cas de cette
  famille dans une seule lecture d'un seul réseau privé.

  **Trois verdicts, jamais confondus** : *le cloud répond différemment* (code 2),
  *l'enregistrement n'a pas pu être réémis tel quel* (un défaut d'instrument,
  jamais compté comme un changement) et *l'appel n'a pas pu être fait* (code 1 —
  un 401, un 429, un 502, ou un garde qui refuse). Le deuxième n'est pas une
  politesse : #73 a trouvé la redaction du proxy fabriquant neuf fausses
  divergences, et #354 quatre de plus, dont trois masquaient tout un cycle de
  vie. Une requête dont le chemin ou une valeur énumérée a été blanchi par
  l'assainisseur n'est donc jamais réémise, une requête portant encore le
  `REDACTED` du proxy non plus, et toute divergence dont la requête porte encore
  une valeur inventée par l'assainisseur est attribuée à l'enregistrement.
  **Cette dernière règle vient d'une mesure, pas d'une inquiétude** : un
  `--dry-run` de `scaleway/terraform.jsonl` contre le vrai compte le 2026-08-21 a
  produit 145 divergences dont aucune n'était le cloud — les créations étaient
  refusées, donc chaque lecture suivante répondait 404 et chaque champ enregistré
  manquait.

  **Ce n'est pas un gate et cela ne doit jamais le devenir.** Il faut des
  identifiants, il crée de vraies ressources, et son verdict dépend du compte qui
  l'a exécuté : trois raisons là où `conformance` en a une. Il tourne à la
  demande (`mise run corpus:cloud`) et sur planification
  (`.github/workflows/corpus-cloud.yml`, le 1er de chaque mois), et ouvre une
  pull request quand quelque chose a bougé, ce qui est la forme que `drift.yml` a
  déjà pour la surface. **Ce workflow est rouge tant que le titulaire du compte
  n'a pas ajouté `SCW_SECRET_KEY`, `SCW_ACCESS_KEY`, `SCW_DEFAULT_PROJECT_ID` et
  `SCW_DEFAULT_ORGANIZATION_ID`**, délibérément : un job qui ne ferait rien en
  silence sans eux rapporterait un succès à chaque exécution et ne mesurerait le
  fournisseur à aucune, ce qui est le SKIP qui ne mesure rien que ce dépôt a
  livré une fois et refuse de livrer à nouveau.

  Mesuré contre le vrai compte Scaleway le 2026-08-21, le jour même où les deux
  fichiers ont été enregistrés : `scaleway/terraform.jsonl` a comparé 16 échanges
  sur 16 et `scaleway/scw-cli.jsonl` 42 sur 58, **zéro divergence disant que le
  cloud avait bougé**, onze attribuées à l'enregistrement (les chemins blanchis
  que `corpus/README.md` documente déjà) et cinq à des routes que cet émulateur
  ne monte pas. Deux faits qu'il vaut mieux ne pas redécouvrir en sont sortis :
  Scaleway accepte un sous-réseau de réseau privé dans `198.18.0.0/15`, l'espace
  synthétique que frappe l'assainisseur, et il accepte la clé `ssh-ed25519`
  synthétique dont le matériel est entièrement à zéro — l'un ou l'autre refusé
  aurait rendu tout un cycle de vie irrejouable.

- **Le vieillissement d'un corpus est maintenant mesuré et non plus deviné** (la
  question ouverte de #353, tranchée). Une exécution qui trouve le fournisseur
  répondant différemment écrit `cloud_moved_at` et `cloud_moved` dans l'entrée de
  cet enregistrement dans `corpus/accepted.json` (`--mark-stale`), et `corpus
  --check` avertit alors avec une date mesurée au lieu de l'horizon de 180 jours
  choisi de mémoire. Il avertit toujours sans jamais échouer, pour la raison
  qu'il l'a toujours fait : le fichier à changer est l'enregistrement, pas
  l'émulateur.
- **Un corpus d'un vrai compte Outscale, machine comprise** (#354, #352, #353).
  `corpus/outscale/oapi-cli-lifecycle.jsonl` porte 179 échanges enregistrés le
  2026-08-21 contre le compte et la région que le propriétaire a nommés
  lui-même, à travers `feint proxy --forward` : les lectures de catalogue que
  toute pile fait d'abord, une paire de clés importée, un Net et son étiquette,
  un Subnet modifié sur place, deux groupes de sécurité portant chacun une règle
  (l'une sur `0.0.0.0/0`, l'autre nommant l'autre groupe), une table de routage
  liée au subnet, un service internet et une route par défaut, **une machine**
  créée avec `BootOnCreation=false` et jamais démarrée, une IP publique liée,
  un volume de 1 Gio attaché puis instantané, une seconde NIC, un service NAT,
  un load balancer interne, deux refus délibérés, et la destruction de tout
  cela, chaque destruction prouvée par une lecture. Rien n'est resté : les
  inventaires d'avant et d'après sont identiques, famille par famille.

  C'est aussi la réponse à une question que `docs/proxy.md` laissait ouverte par
  écrit, celle de ce qu'un identifiant **valide** obtient à travers le tunnel :
  le code 4120 est le même pour une clé inconnue et pour une mauvaise région, et
  ne pouvait donc pas trancher. Il obtient 200, avec de vraies données, en
  lecture comme en écriture.

- **Le pack Outscale déclare ce qu'un rejeu peut comparer** (#354). Cinq
  `ReplayInvariant` : le type de VM que rend une création, la plage d'adresses
  que rendent un Net et un Subnet, et **l'ordre des groupes de sécurité d'une
  machine** sur `UpdateVm` et sur les lectures qui suivent. Le pack n'en
  déclarait aucun, et la conséquence est exactement la forme de défaut que ce
  dépôt traque : `feint corpus --check` imprimait « 0 divergent finding(s) »
  au-dessus d'une exécution où aucune valeur ni aucun ordre d'une réponse
  Outscale n'avait jamais été comparé. Ses compteurs sont passés de
  `values_checked=2, orders_checked=6`, tous Scaleway, à **5 et 56**.

  Le premier ordre comparé a trouvé un défaut (#379), et il tranche un point que
  la garde évidente aurait pris à l'envers : **le cloud ne rend pas l'ordre que
  le client a nommé**. La requête envoyait web puis db, le cloud a répondu db
  puis web. Ce qu'un rejeu peut donc exiger, c'est l'ordre que le *cloud* a
  rendu.

- **Le pack Outscale se porte garant des listes fermées qu'il valide** (#354).
  `PublicVocabulary` publie désormais chaque région et chaque sous-région du
  catalogue, les deux sens d'une règle de groupe de sécurité, les deux natures
  de load balancer et les quatre protocoles d'écouteur : exactement les valeurs
  pour lesquelles une requête est refusée par leur nom. Sans cela, le
  désinfecteur remplaçait `cloudgouv-eu-west-1a` par une chaîne synthétique,
  `knownSubregion` la refusait (l'invariant #269 faisant son travail) et
  `CreateSubnet` répondait 400 là où le cloud répondait 200, emportant avec lui
  la machine, le volume, la NIC, l'IP publique, la liaison de table de routage,
  le service NAT et le load balancer. Falsifié dans
  `tools/falsify/specs/outscale-corpus.json`.

- **`--forward` peut dire où atterrit vraiment un hôte terminé** (#357). Une
  entrée de `feint proxy --forward` peut désormais nommer sa cible — `--forward
  'api.scaleway.com=http://127.0.0.1:4599'` — de sorte qu'un client dont
  l'endpoint est compilé en dur est terminé, enregistré, puis réémis vers
  **l'émulateur** au lieu du vrai cloud. Un hôte écrit sans cible continue
  d'aller au vrai, donc rien ne change pour un appelant existant, et les deux
  formes se mélangent dans une même passe.

  C'est la combinaison qui ne s'exprimait pas : `--forward` enregistrait un
  client qu'on ne peut pas rediriger mais l'envoyait au vrai cloud, et
  `--upstream` envoyait tout à un hôte choisi mais exigeait un client
  redirigeable. Enregistrer contre l'émulateur un client à endpoint compilé
  demandait un espace de noms utilisateur + montage + réseau, un `/etc/hosts`
  remplacé dedans, un écouteur sur le port 443 dans cette pile privée et un
  second étage de proxy — 89 lignes de shell. Il faut maintenant deux variables
  d'environnement et un `=`. Prouvé avec un vrai client le 2026-08-21 :
  `terraform apply` d'un `scaleway_object_bucket`, dont l'endpoint S3 est
  compilé dans le provider, enregistré contre un émulateur feint sans espace de
  noms, sans édition de `/etc/hosts` et sans port privilégié.
  `tools/conformance/forward.sh` rejoue le mécanisme à la demande avec `scw` et
  sans compte : il pointe le CLI sur `https://api.scaleway.test`, un TLD réservé
  qui ne résout jamais, de sorte qu'une passe où le proxy n'est *pas* ce qui
  porte le trafic échoue sur `no such host` au lieu d'atteindre un cloud.

  **Ce n'est pas `--upstream` déguisé, et la différence est l'enregistrement.**
  `--upstream` envoie chaque requête au même endroit quoi qu'ait demandé le
  client, ce qui perd l'information même pour laquelle on enregistre. Ici l'hôte
  demandé est conservé dans le transcript, seule la socket bouge. L'en-tête
  `Host` sortant est la seule chose qu'une entrée mappée déplace, et il le faut :
  la garde anti-DNS-rebinding de feint répond 403 à un `Host` qu'elle ne
  reconnaît pas, si bien que transmettre `api.scaleway.com` tel quel à
  l'émulateur produisait un transcript de refus — mesuré avant le correctif, sur
  l'apply ci-dessus. Une entrée nue reste intacte, donc une signature SigV4 qui
  couvre `Host` se vérifie toujours face au vrai hôte.

  **Les quatre exigences de sécurité de #336 tiennent sans changement, et chacune
  est falsifiée de nouveau à cette porte** (`tools/falsify/specs/forward-proxy.json`,
  21 mutations désormais) : la redaction survit à un tunnel mappé,
  `--expose-to-network` reste refusé avec `--forward`, l'autorité reste éphémère
  et jamais installée, et `--forward '*=<cible>'` est refusé exactement comme
  `--forward '*'` — le joker est cherché dans l'hôte *après* découpe du `=`,
  c'est-à-dire là où un mappage aurait pu faire du recorder un mouchard sans que
  personne le voie. Une cible nomme une socket et rien d'autre : un chemin, une
  requête ou des identifiants sont refusés, sinon le proxy réécrirait chaque
  requête d'une façon que son propre transcript ne montre pas. Aucun drapeau
  nouveau : la surface CLI gelée (version 9) ne bouge pas.

  Un hôte intercepté mais non nommé est désormais diagnostiqué comme l'entrée
  manquante qu'il est, dans la forme que prend le drapeau, plutôt que comme un
  échec de connexion nu — le cas qui coûte une après-midi, puisqu'une famille
  d'API peut vivre sur un autre hôte que le principal (l'API Kubernetes managée
  d'Outscale le fait).

- **Mesuré : le client S3 de Terraform pour Scaleway honore `HTTPS_PROXY`**
  (#346). Cette entrée n'implémente rien ; elle répond à la seule question sur
  laquelle reposait le refus de l'object storage. Il l'honore. Mesuré sous Linux
  le 2026-08-21 — terraform 1.15.4, `scaleway/scaleway 2.81.0` — un apply de
  `scaleway_object_bucket` a émis `CONNECT
  feint-346-measurement.s3.fr-par.scw.cloud:443` et son `CreateBucket` a atterri
  sur un émulateur feint, User-Agent `aws-sdk-go-v2/1.43.4 … api/s3#1.107.0
  terraform-provider-scaleway/2.81.0`. Aucun compte, aucun endpoint réel, les
  identifiants factices publics.

  **Les deux négatifs sont distingués, parce qu'un seul ferme la porte.** Une
  passe témoin contre un proxy qui ne nomme *pas* l'hôte S3 a enregistré zéro
  échange, terminé zéro tunnel et refusé douze connexions vers cet hôte,
  terraform rapportant `request send failed … Forbidden` : c'est « arrivé, puis
  refusé ». L'autre négatif — « n'a pas honoré le proxy » — n'aurait montré
  aucun `CONNECT` et une plainte de certificat, ce que produit macOS, et c'est
  pourquoi la mesure a été faite sous Linux.

  La conséquence est écrite là où vit la décision, `docs/limits.md` (numéro 7),
  avec le transcript : la redirection DNS/TLS que #76 appelait la moitié
  difficile coûte désormais deux variables d'environnement portées par le
  processus, **sur le client même qui la bloquait**. L'object storage reste
  refusé, et le refus tient désormais par son seul argument de couverture — un
  produit sur un client, et une surface S3 que personne n'a chiffrée — et non
  par une quelconque difficulté de la redirection. Le rouvrir est une issue en
  soi, avec ses propres chiffres.

- **L'ACL réseau d'un VPC Scaleway est servie, et les vingt autres refus de
  `vpc/v2` ont été mesurés au lieu d'être supposés** (#343). `vpc/v2/API.GetACL`
  et `vpc/v2/API.SetACL` sont montées en `GET` et `PUT
  /vpc/v2/regions/{region}/vpcs/{vpc_id}/acl-rules`, un jeu de règles par
  famille d'adresses. Le produit `vpc` de Scaleway passe de 17 à 19 opérations
  servies, et de 20 à 18 déclinées.

  **Ce qui a tranché, c'est un enregistrement, pas la forme du SDK.** Les deux
  étaient déclinées avec les cinq règles d'entrée sous une seule raison, « un
  filtre enregistré et jamais appliqué est indiscernable d'une protection »,
  écrite avant que quoi que ce soit ait mesuré qui appelait. Pilotées à travers
  `feint proxy --record` le 21 août 2026, puis classées par
  `feint coverage --observed`, dont c'est le premier usage réel :

  - `scw vpc rule get` adresse `/vpcs/{id}/acl-rules` et a pris un **501** ; le
    provider Terraform officiel 2.81.0 livre `scaleway_vpc_acl` en ressource et
    en source de données ; et un vrai module tiers,
    tf-scaleway-modules/terraform-scaleway-network @ 99f390bb, déclare cette
    ressource dans son propre exemple `complete`. Servies.
  - les cinq opérations `*IngressRule` montrent **zéro** appel observé : `scw`
    n'a pas de sous-commande de règle d'entrée, et aucune stack recensée ne
    nomme `scaleway_vpc_ingress_rule`. Toujours déclinées, et leur raison le dit
    maintenant au lieu de le supposer.
  - les cinq opérations `*VPCConnector`, elles, **ont été appelées** : `scw vpc
    vpc-connector list` et `create` ont toutes deux été enregistrées prenant un
    501. Et elles restent déclinées. Appairer deux VPC est la seule propriété
    que le mode pont ne sait pas livrer, donc y répondre reviendrait à déclarer
    fait ce qui n'a jamais été séparé. La demande décide de ce qui vaut la peine
    d'être servi ; elle ne décide jamais de ce qui peut l'être honnêtement, et
    la raison porte désormais les deux moitiés.

  **Ce qui est servi est un enregistrement, et `docs/limits.md` le dit** dans
  les mots qu'il emploie déjà pour une route personnalisée : le jeu de règles
  fait l'aller-retour, les protocoles et les actions sont tenus aux énumérations
  du SDK, les sources et les destinations sont analysées comme des CIDR, et
  aucun mode de runtime ne programme un filtre en bordure de VPC. Un 501 ne
  protégeait personne sur la question de l'application effective, et arrêtait
  toute stack qui déclare la ressource.

  **La réponse vide est mesurée.** `scw vpc rule get` est une lecture qu'un
  client fait avant d'avoir rien posé, et un VPC sans ACL répond
  `{"rules":[],"default_policy":"accept"}` : lu sur le vrai cloud le 21 août
  2026, sur le VPC par défaut du compte, sans rien créer. La valeur par défaut
  du SDK pour `Action` est `unknown_action`, le zéro protobuf, et ce n'est pas
  ce que porte le fil.

  Prouvé par les deux clients officiels : `scw vpc rule get/set` relit le jeu de
  règles par l'autre porte, et OpenTofu avec terraform-provider-scaleway 2.81.0
  applique `scaleway_vpc_acl`, replanifie **à vide**, le met à jour en place,
  replanifie à vide encore, et le détruit.

- **Un enregistrement Scaleway réel porte enfin une ressource facturée, donc les
  comparaisons de valeur et d'ordre s'exécutent** (#343, sur la chaîne de #352).
  `corpus/scaleway/scw-instance.jsonl` est la création, la lecture, la mise à
  jour et la suppression d'une DEV1-S avec une IP flexible sur un vrai compte
  `fr-par` : trois secondes d'existence le 21 août 2026, tout détruit, la
  destruction prouvée par une lecture rendant 404, et les inventaires de début
  et de fin identiques famille par famille.

  **Cela bouche un trou que rien ne rapportait.** Tous les `ReplayInvariant` que
  ce dépôt déclare vivent sur `CreateServer`, `GetServer` et `UpdateServer` ; un
  serveur est facturé ; les deux enregistrements gratuits n'en atteignaient donc
  aucun. Le gate tournait avec `values_checked=0` et `orders_checked=0` et
  imprimait « 0 divergent finding(s) » par-dessus, y compris pour l'ordre de
  `Server.public_ips`, qui est #320, un défaut qui a coûté une pull request. Le
  même corpus exécute maintenant **2** comparaisons de valeur et **6**
  comparaisons d'ordre, et l'ordre correspond.

  `feint corpus --check` imprime les deux compteurs, et **échoue quand les packs
  déclarent des invariants d'un genre et que le corpus n'en exécute aucun** : un
  contrôle qui n'a jamais eu lieu ne doit pas se lire comme un contrôle qui est
  passé. La condition est la déclaration des packs eux-mêmes, donc un dépôt qui
  ne déclare rien ne se voit rien réclamer.

- **Vingt-six divergences avec le vrai cloud, classées et non corrigées** (#365,
  #366, #367, #368, #369). Le premier enregistrement d'une ressource facturée a
  trouvé cinq causes, chacune portée dans `corpus/accepted.json` avec sa raison
  et l'issue qui supprime son entrée : le volume racine d'une DEV1-S est local
  ici et un volume block sur le cloud, donc la lecture qu'en fait `scw` répond
  404 (#365) ; une réponse serveur omet `bootscript` et `extra_networks`, que le
  cloud écrit `null` et `[]` (#366) ; `image.from_server` vaut `null` ici et une
  chaîne vide sur le fil (#367) ; une IP publique attachée ne publie pas de
  `gateway` et perd ses propres étiquettes (#368) ; et `createServer` honore un
  projet que `listServers` cache ensuite (#369). Classer et corriger dans la
  même passe, c'est ainsi qu'un classement devient ce que le correctif a rendu
  vert par hasard.

- **Exoscale sert le Network Load Balancer, et aucun backend ne porte de
  verdict de santé** (#345, successeur de #14). Toute la famille est montée :
  `exoscale/v2.create-load-balancer`, `exoscale/v2.list-load-balancers`,
  `exoscale/v2.get-load-balancer`, `exoscale/v2.update-load-balancer`,
  `exoscale/v2.delete-load-balancer`,
  `exoscale/v2.add-service-to-load-balancer`,
  `exoscale/v2.get-load-balancer-service`,
  `exoscale/v2.update-load-balancer-service`,
  `exoscale/v2.delete-load-balancer-service`,
  `exoscale/v2.reset-load-balancer-field` et
  `exoscale/v2.reset-load-balancer-service-field`. Exoscale passe de 93 à 104
  opérations servies.

  **Le refus qui rouvre la famille est celui que #14 avait écrit.** #14
  déclinait les onze parce que `load-balancer-service.healthcheck-status` est un
  verdict par backend dont l'énumération vaut `success` ou `failure`, sans
  troisième valeur : un émulateur qui ne sonde aucun backend devrait donc en
  inventer une des deux. Cette lecture était juste sur l'énumération et fausse
  sur le champ. `healthcheck-status` est un **tableau**, et le schéma de ses
  éléments (`load-balancer-server-status`) ne déclare aucune propriété
  obligatoire. Une entrée peut donc nommer un backend sans porter de verdict, et
  c'est ce qui est servi : une entrée par membre du pool d'instances que le
  service vise, chacune avec le `public-ip` qu'un client sonderait, aucune avec
  un `status`. Mesuré avec le CLI officiel : `exo compute load-balancer service
  show` affiche `"healthcheck_status":[{"instance_ip":"192.0.2.2","status":""},
  …]`. Le tableau vide était l'autre candidat, et il est pire : il se lit « ce
  service n'a aucun backend », ce qui est une affirmation sur le pool et non sur
  la mesure. Ce qui n'est *pas* mesuré est dit dans `docs/limits.md` : aucun
  enregistrement d'un NLB vivant n'existe ici, donc la forme de l'entrée vient
  de leur document publié et de deux clients qui l'acceptent, jamais de la
  réponse du cloud lui-même.

  **Aucun plan de données interne, et c'est une mesure, pas un renoncement.**
  #345 demandait si le NLB pouvait être le second client de `machine.Balancer`,
  l'interface neutre bâtie par #315 pour le LBU Outscale. Il ne peut pas, et
  `internal/core` n'a rien gagné pour l'y forcer. Sur une station `incus-ovn`
  vivante le 2026-08-21, sur un réseau OVN créé par l'émulateur (10.63.7.0/24) :
  `EnsureBalancer`, avec l'adresse que ce pack donne à un NLB, répond « listens
  on 192.0.2.1, which is outside … 10.63.7.0/24: an address the runtime has to
  announce goes dark within minutes (#315) » ; le même appel avec 10.63.7.240 et
  un backend répond `<nil>` ; et le démon lui-même refuse l'adresse publique
  avec « Uplink network doesn't contain `"192.0.2.1/32"` in its routes ». Un NLB
  Exoscale publie exactement une adresse, `ip`, et leur schéma n'en déclare
  aucune autre : ni subnet, ni réseau privé, rien qui ressemble au `PrivateIp`
  du LBU. Ce qui manque est donc une adresse, pas un champ de l'interface, et la
  face publique du balanceur reste ce que décrit `docs/limits.md` : une adresse
  TEST-NET-1 qui ne route nulle part.

  **Ce qu'un vrai client a tranché, contre ce qui semblait évident.**
  L'opération que rend une mutation de service référence le **balanceur**, pas
  le service qui vient d'être créé. Référencer le service, ce que fait toute
  autre mutation de ce pack, faisait échouer `terraform apply` sur `Get
  …/v2/load-balancer/<id de service> : resource not found`, parce qu'egoscale v2
  passe cette référence directement à `GetNetworkLoadBalancer` et retrouve le
  nouveau service en comparant la liste du balanceur avant et après
  (`v2/network_load_balancer_service.go:121` en v0.102.4). Le CLI exo ne
  pouvait pas le trouver : il résout chaque objet en listant et en filtrant, et
  ne lit jamais une référence.

  **Un membre de pool porte désormais l'adresse publique que son pool
  déclare**, et `public-ip-assignment` est lu sur le pool. Elle manquait sans
  que personne le voie, parce que personne ne la lisait : les backends d'un
  service sont identifiés par cette adresse, donc des membres sans adresse
  faisaient répondre à chaque service une liste de backends vide. L'allocateur
  TEST-NET-1 du pack compte aussi les balanceurs, si bien qu'un balanceur et une
  IP élastique ne peuvent plus recevoir la même adresse.

  **Prouvé avec de vrais clients.** `tools/conformance/exoscale/exo-cli.sh`
  pilote la création, l'ajout d'un service avec sonde https, la relecture, le
  changement de port, la suppression du service et celle du balanceur, et
  affirme que les entrées de backend existent et ne portent aucun verdict. La
  pile d'exemple `examples/stacks/exoscale/` gagne un `exoscale_nlb` et un
  `exoscale_nlb_service` : avec le provider corrigé que `docs/limits.md` épingle,
  **15 créées, second plan vide, 15 détruites**. Et la pile recensée que le refus
  bloquait — PhilippeChepy/platform, couche `terraform-base`, rejouée le
  2026-08-21 contre un émulateur `de-fra-1` — passe de **19 appliquées / replan
  `6 to add`** à **20 appliquées / replan `5 to add` / 20 détruites** : son
  `exoscale_nlb` s'applique et sa source `data "exoscale_nlb"` se relit. Son
  unique `exoscale_nlb_service` ne s'applique toujours pas, et pas pour une
  raison qui vienne de cet émulateur : il est derrière la branche des buckets
  SOS, qui vise le vrai `sos-de-muc-1.exo.io` et échoue sur des identifiants
  factices, exactement comme le recensement l'avait noté.

  **Les deux remises à zéro par champ portent une raison que nulle autre ne
  porte.** Toute autre famille de ce pack dit que le CLI vide un champ en
  envoyant la mise à jour avec une valeur vide. Mesuré sur celle-ci le
  2026-08-21, il n'en fait rien : `exo compute load-balancer update
  --description ""` envoie `PUT {}`, et la forme service n'envoie que le bloc de
  sonde qu'elle renvoie à chaque appel. Ce CLI ne vide aucun champ, ni par mise
  à jour ni par remise à zéro, et recopier la phrase habituelle aurait consigné
  un comportement qu'il n'a pas.

- **L'émulateur sait refuser, par opération, éteint par défaut** (#26, #356).
  `PUT /_feint/faults` arme une règle qui nomme une opération amont et ce qu'il
  faut répondre à la place : un statut, un délai, ou un corps coupé. `GET` liste
  les règles avec leur nombre de déclenchements, `DELETE` les efface, et un
  émulateur neuf n'arme rien.

  **La mesure qui l'a demandé.** `coverage/evidence.json` porte sept axes par
  opération montée. `negative` tenait à 34 sur 357, loin en dessous de tous les
  autres : cet émulateur prouvait ce que ses routes répondent quand tout va bien
  et presque rien de ce qu'elles répondent quand ça se passe mal, et les chemins
  de dégradation d'un client ne pouvaient être que simulés, dans les tests de ce
  client. #356 a mesuré l'autre bout du même trou : aucun en-tête
  d'authentification répondait `200`, et un jeton bidon aussi.

  **Le noyau décide quand ; le pack décide à quoi ressemble une panne.** Un 503
  atteint un client Scaleway sous la forme d'erreur de `scw`, un client Outscale
  dans son `ResponseContext`, un client Exoscale dans son enveloppe à message nu
  (`emulator.Faulter`, nouvelle et optionnelle). Là où un SDK nomme un `type`
  pour un statut, le pack l'émet — les `permissions_denied` et
  `denied_authentication` de Scaleway, deux cas de `unmarshalStandardError` —
  pour que la dispatch du client se déclenche et que `errors.As` corresponde. Là
  où aucun n'est nommé, personne ici n'a mesuré comment Scaleway écrit un 429, et
  la valeur dit clairement qu'elle appartient à cet émulateur plutôt que de
  publier un fait plausible sur un fournisseur. Une règle dont un pack ne sait
  pas rendre le statut est refusée à l'écriture, jamais servie avec un corps
  inventé par le noyau.

  **Déterministe, ciblée, et refusée tôt.** `times` borne une règle aux N
  premiers appels ; il n'existe aucun réglage probabiliste, parce qu'une faute
  qui se déclenche au hasard ne peut pas être le sujet d'un test. Une règle nomme
  une opération, au nom que publie `/_feint/routes`, et une seule règle par
  opération. Une règle qui nomme une opération que rien ne sert est refusée par
  un 400 : une règle qui ne se déclenche jamais se lit, de l'extérieur, exactement
  comme un client qui a survécu à la faute.

  **Une réponse injectée ne prouve rien**, et c'est un contrôle plutôt qu'une
  promesse. Elle ne déplace aucun compteur sauf le nouveau, `injected` :
  l'opération reste `driven: false`, sa réponse n'est pas contrôlée contre le
  contrat, ses champs ne rejoignent aucune union, et l'émulateur *refuse de
  fermer* une portée d'assertion `negative` dessus, en disant pourquoi.
  `tools/conformance/score.sh` fait échouer toute exécution qui porte une réponse
  injectée : l'exécution de conformance partagée ne peut donc pas être gonflée —
  l'injection de fautes a sa suite, son émulateur et son port.

  **Mesuré avec les vrais clients** (`tools/conformance/faults.sh`, dans
  `mise run conformance` et sur la jambe `fields` du workflow) :

  - `scw` affiche `scaleway-sdk-go: insufficient permissions: GET …` sur un 403
    injecté et `Cannot find resource 'server' with ID …` sur un vrai 404 — la
    distinction dont le consommateur de #356 a besoin, et que rien ici ne savait
    produire ;
  - `scw` **réessaie un 429 et pas un 503** : 429, 429, 200 aboutit, alors que le
    premier 503 termine la commande. Le retry est celui du CLI, pas du SDK :
    `scaleway-sdk-go/scw` n'en contient aucun ;
  - le vrai provider Terraform Outscale survit à **503, 503, 200** dans un
    `apply`, avec deux temporisations de `go-retryablehttp`, et atteint `Apply
    complete` ;
  - `oapi-cli` réessaie les deux (`attempt 0 failed. Retrying in 3520 ms.`) et
    décode le 403 en code `4120`, que lit `osc.IsAuthError` et que `osc.IsNotFound`
    ne lit pas ;
  - `exo` réessaie un 503 cinq fois et aboutit à la troisième réponse, et rend un
    403 en `Forbidden` contre `not found` pour une absence réelle.

  Non livré, et dit plutôt que découvert : les coupures de connexion et les vraies
  pannes de transport vivent sous le handler de route ; un corps coupé est ce qui
  est offert à la place. Le contrôle déterministe de la durée d'une transition
  asynchrone émulée vit dans le cycle de vie d'un pack, pas devant un handler, et
  n'est pas replié ici.

- **Le corpus committé est rejoué à chaque pull request** (#353). `feint corpus
  --check`, `mise run corpus:check`, dans `prepush` et dans
  `.github/workflows/go.yml`. Il rejoue chaque fichier de `corpus/` contre un
  émulateur qui lui est propre et échoue sur une divergence avec ce que le vrai
  cloud a répondu. Trente millisecondes, hors ligne, sans credential ni binaire
  client, parce que toutes ses entrées sont des fichiers versionnés : c'est
  précisément ce qui lui permet d'être un gate là où `conformance` ne peut pas
  l'être, un crochet qui échoue sur un binaire absent enseignant `--no-verify`,
  qui désarme tous les autres d'un coup.

  **C'est le premier contrôle d'ici qui compare une réponse à celle du cloud.**
  `mise run conformance` prouve qu'un vrai client **accepte** ce que l'émulateur
  répond, et ne peut pas prouver que cette réponse est celle que le cloud aurait
  donnée, puisque le cloud n'est pas là. `shapes --check` compare des arbres de
  champs ; un corpus porte aussi le statut, l'ordre et la séquence.

  **Trois verdicts, jamais confondus.** Une divergence que la liste
  d'acceptation ne porte pas vaut 2. Une opération qu'aucune route ne sert est
  rapportée et jamais comptée : le jour où elle fait échouer un build est le
  jour où quelqu'un arrête d'enregistrer. Un corpus illisible, ou qui n'a rien
  comparé, vaut 1 : un fichier vide, un répertoire vide et un fichier dont
  chaque échange est non servi sont rouges, chacun avec son propre message. **Un
  corpus qui ne rejoue rien est un échec, jamais un succès** : ce dépôt a livré
  l'autre forme deux fois, dans la suite réseau et dans cinq contrôles de
  `tools/ui/check-page.py`.

  **`corpus/accepted.json`**, sur le modèle de `tools/compat/accepted.json`,
  porte les huit divergences du premier passage avec leur raison et l'issue qui
  les retire (#355), ainsi que la date d'enregistrement de chaque fichier. Les
  deux moitiés sont tenues : une entrée qui n'excuse rien fait échouer le gate,
  donc un correctif ne peut pas laisser son exemption derrière lui, et un
  fichier de corpus que personne n'a daté le fait échouer aussi.

  **Comment un corpus vieillit : il avertit, il n'échoue jamais.** Un gate qui
  échoue parce que le **cloud** a bougé est un gate qu'on désactive, et
  celui-ci ne tient qu'un côté de la comparaison : il peut dire que l'émulateur
  et l'enregistrement divergent, jamais lequel des deux a changé. Échouer sur
  l'âge affirmerait exactement ce qu'il ne mesure pas ; #359 est la moitié qui
  peut trancher. L'horizon vaut 180 jours, dans un fichier committé, et
  l'avertissement nomme le fichier, son âge et la procédure de réenregistrement.

- **`feint replay` lie un identifiant enregistré sous le nom de champ où il a
  été vu** (#353). Le corpus a fait apparaître un enregistrement que la liaison
  précédente ne savait pas représenter : sur un compte Scaleway à un seul
  projet, `project_id` et `organization_id` sont la **même chaîne**, donc une
  valeur enregistrée avait deux réponses candidates et une table de valeur à
  valeur n'en gardait qu'une. Le vainqueur était décidé par le parcours de map
  aléatoire de Go : six rejeux de `corpus/scaleway/scw-cli.jsonl` contre six
  émulateurs neufs ont classé `vpc/v2/API.ListPrivateNetworks` divergente trois
  fois et conforme trois fois, et quand l'organisation gagnait, la création
  déposait son réseau dans un projet que la liste non filtrée ne couvre pas.
  Une divergence que le rejeu fabriquait lui-même.

  Les liaisons sont désormais portées par le nom de champ où elles ont été
  observées, donc `project_id` se résout vers le projet que cet émulateur a
  frappé et `organization_id` vers l'organisation, et le parcours qui les
  apprend est trié, de sorte qu'une valeur sans champ pour la porter, un segment
  de chemin, se résout de la même façon à chaque exécution. La table non portée
  reste le recours, et c'est elle qui garde un chemin relié. Une exécution
  rapporte maintenant combien de valeurs enregistrées deux champs ont liées
  différemment, au lieu de les arbitrer en silence. Le verdict sur le corpus
  committé est passé de « 8 ou 9 » à 8 sur huit exécutions.

- **La surface CLI gelée passe en version 9** (#353) : le verbe `corpus`, avec
  `--check`, `--dir` et `--accepted`. Un ajout, rien n'a été retiré, aucun code
  de sortie n'a bougé, et un pipeline calé sur la version 8 continue de marcher.

- **`feint replay` rejoue un enregistrement ici et dit ce qui diverge** (#73).
  `feint replay run.jsonl --endpoint http://127.0.0.1:4599` reprend chaque
  requête enregistrée, l'envoie à un émulateur qui tourne, et rapporte opération
  par opération. Trois verdicts, jamais additionnés : **conforme**,
  **divergent**, et **non servi** — ce dernier étant la file de travail de #74
  et non un échec, pour que le jour où il fait rougir une CI ne soit pas le jour
  où quelqu'un arrête d'enregistrer. Code de sortie 2 sur une divergence, 1
  seulement quand l'outil lui-même a échoué.

  **Ce qui est comparé, et ce qui ne l'est délibérément pas.** Un diff octet à
  octet serait du bruit, donc la comparaison est graduée : le statut exactement,
  les champs présents exactement moins ce que le `DeclinedFields()` d'un pack
  excuse, les types exactement, et les valeurs et l'ordre *seulement* là où un
  pack les déclare comparables (`emulator.Invariant`, nouveau). Les deux
  derniers sont des défauts que ce dépôt a déjà payés : #270 a mesuré deux
  créations `vpc/v2` répondant 201 là où le cloud répond 200, ce que la ligne du
  statut attrape sans compte Scaleway ; #320 a mesuré `Server.public_ips`
  revenant dans l'ordre du store plutôt que dans celui que la création avait
  nommé, ce que *seule* la ligne de l'ordre attrape. Le pack Scaleway déclare cet
  ordre pour `CreateServer`, `GetServer` et `UpdateServer`, plus les deux
  valeurs qu'un client nomme toujours à la création.

  **Les identifiants sont réassociés, pas comparés.** Une exécution enregistrée
  adresse les objets que le cloud a créés pour elle, et cet émulateur crée les
  siens. Le replay apprend donc, de chaque réponse, quel identifiant enregistré
  l'émulateur a répondu à sa place, et le substitue dans chaque requête
  suivante : segments de chemin entiers, valeurs de paramètre entières, chaînes
  de corps entières, et seulement pour des valeurs qui ont la forme de ce qu'un
  cloud distribue (un UUID, une adresse, un `i-<hex>` Outscale). Sans cela, le
  cas d'identité que #73 place en premier est inatteignable : un transcript
  enregistré contre l'émulateur se rejoue contre une instance **neuve** avec zéro
  divergence, là où chaque lecture répondrait sinon 404.

  **Rien de l'enregistrement ne ressort.** Un transcript est expurgé de ses
  identifiants d'accès et n'est **pas** anonyme : `docs/proxy.md` énonce, champ
  par champ, que les corps portent l'inventaire d'un compte. Un constat nomme
  donc un chemin, un type, un statut et une *position* : une liste désordonnée
  est rapportée « 0,1 répondu 1,0 », jamais en nommant les identifiants qui ont
  bougé, et le chemin de la requête est anonymisé avant d'être imprimé.

- **`feint coverage --observed` classe ce que les packs déclinent par ce qu'un
  client a réellement appelé** (#74). Chaque refus de ce dépôt porte sa raison,
  ce qui est la discipline ; aucun ne porte une *demande*, ce qui était le trou.
  À partir d'un enregistrement, ou d'un répertoire d'enregistrements, la vue
  liste les opérations déclinées qu'un vrai client a appelées quand même, la plus
  appelée en premier, chacune avec son propre argument à côté et la famille de
  client qui a produit les appels.

  Deux faits sont comptés à part et jamais additionnés : **personne ne l'a
  appelée** et **personne ne l'a triée**. Les confondre est le défaut que cette
  vue existe pour corriger, donc le rapport énonce les deux populations dans des
  mots qui ne peuvent pas se lire l'un pour l'autre, et une opération que
  personne n'a appelée est un compte, pas une ligne — un classement qui porte
  tous les refus, c'est l'alphabet de nouveau.

  Elle réclame `--contract`, et c'est ce qui la rend possible : `feint proxy`
  nomme un échange à partir des *routes montées*, donc un appel vers une
  opération déclinée ne porte aucun nom, et seul le document du fournisseur peut
  dire que `GET /v2/dns-domain` est `list-dns-domains`. `feint coverage` sans
  `--observed` rend exactement ce qu'il rendait avant : la vue observée remplace
  le rapport au lieu de s'y ajouter, donc `--format json` continue de produire
  l'artefact versionné octet pour octet et `tools/drift/gate.sh` n'est pas
  touché.

  Mesuré sur un enregistrement de `scw`, `exo`, `oapi-cli` et `terraform`
  pilotant cet émulateur à travers le proxy : un refus Exoscale
  (`list-dns-domains`, 7 appels d'`exo`) et deux Outscale (`ReadApiAccessRules`
  et `ReadCatalog`, d'`oapi-cli`).

- **`feint transcript --sanitise` transforme l'enregistrement d'un vrai cloud en
  artefact que ce dépôt peut committer** (#351). Un transcript porte l'inventaire
  du compte de quelqu'un, et `shapes/*.json`, la seule chose committable qu'on
  tirait d'un enregistrement, jette exactement ce qu'un replay note au-delà de
  l'arbre de champs : le statut, l'ordre, et la séquence elle-même. `feint
  replay` n'avait donc jamais rencontré autre chose que sa propre sortie.

  ```bash
  feint transcript real.jsonl --sanitise corpus/scaleway/scw-cli.jsonl \
    --contract contracts/scaleway.json
  ```

  La sortie est un transcript comme un autre, relu tel quel par `replay`,
  `transcript` et `shapes`, dont **chaque valeur est remplacée par une valeur
  synthétique de même forme** : un UUID devient un UUID, une adresse une adresse,
  un CIDR un CIDR de même longueur de préfixe, une clé OpenSSH une clé OpenSSH
  valide. C'est ce qui le laisse rejouable, le replay reliant un identifiant
  synthétique exactement comme il relie celui qu'un cloud a distribué, là où une
  valeur écrasée par `REDACTED` casserait la requête qui la porte et retyperait
  le champ qui la tient, ce qui est le défaut que la redaction du proxy avait
  produit dans #73.

  **Refus par défaut.** Une redaction par *nom* répond « est-ce que ça ressemble
  à un secret », jamais « est-ce que ça n'en est pas un ». Une valeur ne survit
  donc que si ce dépôt la publie lui-même : un littéral du chemin écrit par le
  document du fournisseur, un mot dont un pack se porte garant
  (`emulator.Vocabulary`, nouveau : les zones et régions de Scaleway, les deux
  listes pour lesquelles il répond 400), une valeur que le contrat énumère, un
  booléen, une suite d'au plus six chiffres. Un chemin que le document ne décrit
  pas perd donc **tous** ses segments, et la commande énumère ce qu'elle a effacé
  au lieu de garder les mots qui semblaient inoffensifs.

  **Deux contrôles, tous deux avant que le fichier existe**, et rien n'est écrit
  si l'un des deux parle : la sortie est recoupée avec l'enregistrement source,
  donc une valeur qui a survécu est nommée quelle qu'ait été sa forme ; et chaque
  valeur de la sortie doit appartenir à l'alphabet qu'un transcript assaini a le
  droit de porter. Deux tests de plus relisent les fichiers committés, dont un
  par la seule forme, sans rien savoir des règles qui les ont produits.

  Falsifié dans les deux sens, douze mutations
  (`tools/falsify/specs/sanitised-corpus.json`) : retirez une part quelconque de
  la substitution et une valeur du compte est publiée, ou l'audit se tait ;
  retirez ce dont un pack se porte garant et le corpus assaini rejoue **400 zone
  inconnue** sur chaque appel, la pièce de musée que le second sens existe pour
  éviter.

- **Un corpus committé de ce que le vrai Scaleway répond** (#352), sous
  `corpus/`, rejouable avec `feint replay`. Enregistré le 21/08/2026 à travers
  `feint proxy` contre un vrai compte en `fr-par`, en pilotant
  terraform-provider-scaleway 2.81.0 et `scw` 2.56.3 : le cycle complet
  création, lecture, mise à jour, destruction d'un VPC et d'un réseau privé, les
  lectures que toute stack fait avant de créer quoi que ce soit, une clé SSH IAM,
  et deux 404 délibérés. Ressources gratuites uniquement, tout détruit sous un
  `trap`, chaque destruction prouvée par une lecture qui a répondu 404, et le
  compte identique au bit près avant et après sur quinze familles d'objets.

  Ce qu'il a mesuré, sur l'émulateur du jour : l'enregistrement Terraform
  concorde sur ses 16 échanges, la première fois qu'un replay rencontre la
  réponse d'un vrai cloud et lui donne raison, et celui de `scw` sur 33 des 58,
  avec 16 opérations qu'aucun pack ne sert et 8 ou 9 divergences en trois
  familles. Le « ou » est une mesure lui aussi : six exécutions du même fichier
  ont noté `ListPrivateNetworks` divergente trois fois et concordante trois
  fois, parce que sur ce compte `project_id` et `organization_id` sont la même
  chaîne, que la reliaison du replay a deux candidats pour elle, et qu'elle
  parcourt une map Go pour choisir. `corpus/README.md` nomme tout cela, ainsi
  que les règles de compte pour enregistrer le fournisseur suivant.

- **Un pack peut déclarer ce qu'un replay compare au-delà de la présence et du
  type** (`emulator.Invariant`, `ReplayInvariants()`). Optionnel à la manière de
  `FieldDecliner`, avec une raison tenue au garde que `Declined()` affronte, plus
  un garde propre : un genre que rien n'implémente est refusé au lieu de se lire
  « comparé ». Une déclaration qui nomme une opération qu'aucune route ne sert
  fait échouer un test. Le rapport compte séparément les contrôles de valeur et
  ceux d'ordre, pour qu'une déclaration qui n'a rien évalué ne puisse pas se lire
  comme une déclaration tenue.

- **Deux corpus de plus d'un vrai cloud : Exoscale depuis un compte nommé,
  Outscale depuis aucun compte du tout** (#354, après #351/#352/#353). `corpus/`
  porte désormais quatre fichiers et 264 échanges, et `mise run corpus:check`
  les rejoue tous hors ligne, un fichier à la fois, contre un émulateur neuf.

  `corpus/exoscale/exo-cli.jsonl` : 203 échanges, `exo` 1.95.1 (egoscale
  v3.1.36) contre un vrai compte Exoscale en `ch-gva-2` le 2026-08-21, à travers
  `feint proxy --forward '*.exoscale.com'`. Il porte les lectures que toute
  stack fait avant de créer quoi que ce soit (zones, types de machines,
  templates sous un filtre `visibility` explicite, clés SSH, groupes de
  sécurité, groupes d'anti-affinité, réseaux privés, instances, pools, IP
  élastiques, block storage, load balancers, quotas), deux 404 délibérés, et
  tout le cycle de vie gratuit : enregistrer, lire, lister et supprimer une clé
  SSH, créer deux groupes de sécurité avec une règle chacun (l'une sur
  `0.0.0.0/0`, l'autre nommant l'autre groupe), créer un groupe d'anti-affinité,
  et créer, lire, modifier, relire puis supprimer un réseau privé. **Rien de
  facturé n'a été créé** : une instance, une IP élastique, un volume block
  storage, un NLB et un cluster SKS sont tous payants, et aucun n'a été fait.
  Chaque suppression est prouvée par une lecture rendant 404 ou une liste vide,
  et le compte finit comme il a commencé, un groupe de sécurité `default` et
  rien d'autre.

  `corpus/outscale/oapi-cli-catalogue.jsonl` : 5 échanges, `oapi-cli` 0.13.0
  contre `api.eu-west-2.outscale.com`, **pilotés avec les identifiants
  placeholders publics de `tools/conformance/outscale/fake-credentials.env`**.
  Mesuré le 2026-08-21 : cinq opérations répondent 200 à une clé d'accès
  inconnue, `ReadRegions`, `ReadVmTypes`, `ReadPublicIpRanges`,
  `ReadPublicCatalog` et `ReadFlexibleGpuCatalog`, là où toutes les opérations
  authentifiées répondent 400 `InvalidParameterValue` 4120. Le catalogue d'un
  fournisseur est donc enregistrable depuis n'importe quelle station, sans
  compte à risquer et sans inventaire dans les réponses, et c'est la première
  fois que ce dépôt compare son pack Outscale au **cloud** plutôt qu'à un
  document. Trois des cinq se rejouent sans divergence ; les deux lectures de
  catalogue que rien ne sert sont la file de #74.

  **Ce que l'enregistrement Exoscale a trouvé : trois champs que le cloud répond
  et que cet émulateur omet**, chacun avec une exemption dans
  `corpus/accepted.json` nommant l'issue qui la supprime. `zones[].id` sur toute
  liste de zones (#370, 51 constats, l'opération la plus répondue du fichier) ;
  `visibility` sur un groupe de sécurité, sur la liste comme sur la lecture
  unitaire (#371, 46) ; et `rules[].security-group.name` quand une règle nomme
  un autre groupe (#371, 8). Les trois ont une racine commune qui mérite d'être
  dite : **`contracts/exoscale.json` ne les déclare pas non plus**, donc le gate
  des formes, la sonde et le pack sont d'accord entre eux parce qu'ils lisent le
  même document, et le document est en retard sur le cloud. Seul un
  enregistrement de fil pouvait être en désaccord. C'est la famille du
  `has_s3_integration` de #352, un fournisseur plus loin.

  **Les règles de compte de #352 ont tenu sans exception**, et #354 en ajoute
  une qui ne parle pas d'argent : *le profil est nommé explicitement à chaque
  commande, et une région dont la raison d'être est une frontière de conformité
  n'est jamais une cible*. Le compte Exoscale est nommé par `exo -A <compte>` sur la
  soixantaine d'appels du script d'enregistrement, et lequel c'était figure dans
  la pull request plutôt qu'ici ; le run Outscale nomme son
  unique profil factice par `--profile`, de sorte qu'il n'existe aucun défaut
  vers lequel retomber et aucun profil stocké de la station qui puisse être
  présenté. `corpus/README.md` porte les deux procédures.

### Sécurité

- **Un rejeu contre un vrai compte refuse de toucher ce qu'il n'a pas créé**
  (#359). Tout identifiant dans le chemin d'une requête qui change l'état doit
  être un identifiant que les créations de cette exécution ont produit : c'est
  `mustOwn` appliqué là où se tromper détruit le bien d'autrui, et il le faut
  parce qu'une requête enregistrée est bien formée par construction, ce qui n'a
  jamais été la même question qu'autorisée. Une création est refusée si son
  opération n'est pas sur une liste écrite de ce qui ne coûte rien, et le refus
  nomme l'opération, pour que le rapport dise quelle mesure est hors de portée
  sans dépense au lieu de dépenser pour le découvrir. Tout ce qui est créé
  s'appelle `feint-corpus-*` et est détruit à la sortie depuis un registre armé
  avant le premier appel, **chaque destruction étant prouvée par une lecture qui
  répond 404** et non par le code de retour du delete ; un objet qui survit fait
  échouer l'exécution quelle qu'ait été la comparaison. Une clé secrète voyage
  par variable d'environnement et jamais dans argv, et est refusée à un point
  d'entrée en HTTP clair hors loopback.

  **Vingt-deux mutations, toutes falsifiées**
  (`tools/falsify/specs/corpus-cloud.json`, exécution du 2026-08-21) : retirer la
  question de propriété et un `DELETE` enregistré supprime le VPC du compte ;
  retirer la liste des créations gratuites et le compte est facturé pour une
  mesure ; croire le code de retour du delete et un fournisseur qui supprime en
  asynchrone laisse l'objet derrière lui sous une exécution verte. Contre le vrai
  compte le même jour, cinq objets ont été créés et cinq détruits avec chaque
  destruction prouvée par une lecture, et l'inventaire pris avant chaque
  exécution correspondait à celui pris après.

### Modifié

- **Le registre suit les lots de divergences et de file** (#431, #445), et
  `mise run conformance:exoscale-terraform` arme son `trap` avant l'émulateur
  qu'elle démarre. En trois étapes elle en laissait un derrière elle quand le
  script échouait, jusqu'à ce qu'`evidence:update` refuse de démarrer à côté :
  le pas de la porte ajouté par #426, appliqué à la tâche qui venait d'être
  écrite. Une tâche qui démarre un processus possède sa mort sur tous les
  chemins, pas seulement sur le chemin heureux.

- **Le registre suit le correctif de la réponse vide** (#429) : `contract` passe
  de 330 à **361 sur 370** et `probed` de 330 à **359**. Le `contract` de
  Scaleway atteint **173 sur 173** et son `probed` 170. **Aucune opération n'a
  rien perdu sur aucun axe**, vérifié opération par opération contre le registre
  remplacé. Les trente et une opérations qui gagnent `contract` étaient déjà
  justes : personne n'avait jamais regardé, ce qui est le sens de `unchecked` et
  la raison pour laquelle ce n'est pas `absent`.

- **Le registre suit le lot de refus sur Scaleway et Exoscale** (#428) :
  `negative` passe de 197 à **247 sur 370**. Celui de Scaleway de 97 à **139 sur
  173**, celui d'Exoscale de 10 à 18. **Aucune opération n'a rien perdu sur aucun
  axe**, vérifié opération par opération contre le registre remplacé plutôt qu'en
  comparant des totaux.

- **Le registre suit le lot de refus Outscale** (#440) : `negative` passe de 173
  à **197 sur 370**, et celui d'Outscale de 66 à **90 sur 93**. `behaviour` de
  319 à 320. **Aucune opération n'a rien perdu sur aucun axe**, vérifié
  opération par opération contre le registre remplacé plutôt qu'en comparant des
  totaux. Douze des zéros Outscale restants sont déclarés hors d'atteinte à la
  route, avec une raison que la garde remesure.

- **Le registre est régénéré sur les nouveaux enregistrements, et `shape` vaut
  225 sur 370** (#427). Outscale atteint **93 sur 93**, son cinquième axe complet ;
  Scaleway passe de 37 à 99, Exoscale de 31 à 33. Le tableau par fournisseur
  vit dans `docs/routes.md`.

  **Six opérations perdent l'axe, et c'est la correction plutôt qu'une
  régression** : les six sont des `DELETE` qui l'avaient gagné par un champ
  fantôme écrit au chemin vide par un `204`. Elles sont nommées —
  `DeleteVolume`, `DeleteSSHKey`, `DeleteIP`, `DeleteServer`,
  `DeletePrivateNetwork`, `DeleteVPC` — et vérifiées opération par opération
  contre le registre remplacé plutôt qu'en comparant des totaux, parce qu'un
  total peut cacher une perte sous un gain. Aucun autre axe n'a bougé pour
  aucune opération.

- **Le registre est régénéré après que les suites ont gagné les appels que le
  repli a fait apparaître** (#407) : `driven` de 344 à 345, `dataplane` de 344 à
  345, `behaviour` de 316 à 317. **Aucune opération n'a rien perdu sur aucun
  axe**, vérifié opération par opération contre le registre remplacé plutôt
  qu'en comparant des totaux. `shape` reste à 134, ce que le repli avait mesuré
  et non un second tirage.

- **Le registre de preuves est régénéré sur l'attribution causale, et
  `behaviour` vaut 316** (#398). Le registre commité portait 312, un tirage fait
  avant qu'une écriture dans le store soit créditée à la requête qui l'a faite.
  Outscale passe de 77 à 79, Scaleway de 157 à 159, et **aucune opération n'a
  rien perdu sur aucun axe** : vérifié opération par opération contre le registre
  remplacé, et non en comparant des totaux, parce qu'un total peut cacher une
  perte sous un gain. Les six autres axes sont inchangés à l'opération près.

  Les deux jambes ont tourné sur une station au calme, machines allumées pour la
  seconde, et l'hôte a été relu ensuite : aucun conteneur ni réseau de
  l'exécution ne subsistait.

- **`internal/replay` a gagné les deux coutures qu'un vrai compte exige, et rien
  d'autre** (#359). `Options.Guard` est consulté avant chaque envoi — sur la
  requête *après* rebinding, la seule forme qu'il vaille la peine de juger,
  puisqu'un `DELETE` enregistré nomme l'identifiant que le cloud a frappé la fois
  précédente — et reçoit chaque réponse. `Options.Bind` amorce la table de
  rebinding avant la première requête, ce qui est ce qui rend rejouable un
  enregistrement qui s'ouvre sur une création :
  `corpus/scaleway/terraform.jsonl` commence par un POST portant un `project_id`
  qui n'appartient à aucun compte visé. Un échange refusé est `Refused` : jamais
  une correspondance, jamais une divergence, parce que l'appel n'a pas été fait.
  Un rejeu contre cet émulateur ne passe ni garde ni amorce, donc `feint corpus
  --check` et `feint replay` sont inchangés.

- **La surface CLI passe en version 10.** `corpus` gagne `--against-cloud`,
  `--file`, `--endpoint`, `--credential`, `--bind`, `--format`, `--timeout`,
  `--dry-run` et `--mark-stale`. Des ajouts sur un verbe existant ; rien n'a été
  retiré, aucun code de sortie n'a bougé, et un pipeline calé sur la version 9
  continue de fonctionner.

- **Surface CLI version 8.** Toutes les entrées ci-dessus sont des ajouts : le
  verbe `replay` avec `--endpoint`, `--format` et `--timeout`, `coverage
  --observed`, et `transcript --sanitise` avec `--contract`. Rien n'a été retiré
  et aucun code de sortie n'a bougé, donc un pipeline calé sur la version 6
  continue de fonctionner.

- **`/_feint/conformance` version de schéma 4, et une cinquième surface gelée.**
  La charge utile gagne `injected`, les réponses produites par l'injecteur de
  fautes, par opération. Additif, et cela change ce que le document *signifie*
  plutôt que ce qu'il porte : tous les autres compteurs décrivent ce que
  l'émulateur a servi, celui-ci nomme ce qu'il a mis en scène. `/_feint/faults`
  est gelée dès sa première version, avec sa propre fixture, parce qu'une suite
  qui arme une faute depuis un fichier versionné est un consommateur dès le
  premier jour. `cliSurfaceVersion` **ne bouge pas** : l'injecteur s'atteint par
  le plan d'administration seul et n'ajoute ni verbe ni drapeau.

- **Surface CLI version 7.** Les deux entrées ci-dessus sont des ajouts : le
  verbe `replay` avec `--endpoint`, `--format` et `--timeout`, et `coverage
  --observed`. Rien n'a été retiré et aucun code de sortie n'a bougé, donc un
  pipeline calé sur la version 6 continue de fonctionner.

- `internal/shape.IsUUID` est exporté, pour que le replay pose à une valeur
  enregistrée la même question que le catalogue de formes pose à un segment de
  chemin. Deux écritures de « est-ce un identifiant » répondraient
  différemment le jour où l'une des deux apprendrait un cas.

- **Les écouteurs d'un load balancer Outscale peuvent bouger après la création**
  (#344) : `osc/Client.CreateLoadBalancerListeners` et
  `osc/Client.DeleteLoadBalancerListeners` sont servis, et le balanceur du
  runtime les suit.

  **Le manque n'a jamais été le premier apply.** `CreateLoadBalancer` porte ses
  écouteurs en ligne, et c'est pour cette raison que les trois stacks Outscale
  recensées qui montent un LBU convergeaient déjà (#281). C'était le *second*
  apply, celui qui modifie un bloc `listeners` sur un load balancer qui existe
  déjà, et les trois versions du provider lues ici appellent la paire depuis
  leur chemin Update et depuis nulle part ailleurs (v1.1.3
  `resource_outscale_load_balancer.go:671,695`, v1.7.0 `:732,745`, v1.8.0
  `resource_load_balancer.go:990,1001`). Mesuré le 2026-08-21 avec le provider
  1.8.0 avant le correctif : déplacer le port d'écoute répondait `Error: Unable
  to update Load Balancer listeners`, portant `feint does not serve
  DeleteLoadBalancerListeners`, et chaque plan suivant restait indéfiniment à
  `0 to add, 1 to change, 0 to destroy`. Après le correctif, sur les providers
  **1.8.0 et 1.1.3 tous les deux** : apply, plan vide, port déplacé, **second
  plan vide**, destroy propre. Et `ReadLoadBalancers` détient exactement
  `[8080]` : l'ancien port a disparu au lieu d'être conservé à côté du nouveau.

  **Le plan de données suit le plan de contrôle, et cela aussi est mesuré.**
  Sous `--vm incus-ovn`, le balanceur distribue réellement des paquets (#315) :
  un écouteur que l'API déplace pendant que le runtime garde l'ancien port
  serait un nouveau mensonge plutôt qu'une nouvelle fonctionnalité.
  `tools/conformance/outscale/balancer.sh` déplace maintenant l'écouteur et
  vérifie les deux bouts : 8080 répond, servi par la machine enregistrée, et 80
  cesse de répondre. Exécuté le 2026-08-21 contre un vrai runtime OVN : 6/6 à
  t0, 6/6 à t+60s, l'unlink respecté, le déplacement suivi, et l'hôte ne détient
  plus aucun balanceur après la suppression.

  Ce qui rend cela vrai tient en une branche de `syncBalancer` : un balanceur
  qui a perdu tous ses écouteurs est *retiré* du runtime au lieu d'être laissé
  en place. Ce n'est pas un cas limite, c'est le milieu de tout changement de
  port sur un écouteur unique, puisque le provider supprime le port qui part
  avant de créer celui qui arrive. Neutralisez cette branche dans une copie hors
  du dépôt et la suite OVN échoue sur `the balancer does not answer on its new
  port 8080` : c'est la falsification, aux côtés des cinq mutations de
  `tools/falsify/specs/listener-day-two.json`, qui mordent toutes.

### Modifié

- **La moitié déclinée de la famille LBU Outscale est triée en quatre au lieu
  d'une** (#344), parce que les raisons ne sont pas interchangeables et qu'une
  phrase unique disait « aucune stack recensée n'appelle celles-ci » à propos de
  toutes. Désormais : les règles d'écouteur et les politiques de persistance
  relèvent de la **demande**, personne ne les a réclamées ; les étiquettes d'un
  load balancer après sa création sont **le mur suivant, nommé**, mesuré le
  2026-08-21 (le provider 1.8.0 répond `Error: Unable to update Load Balancer`
  sur `DeleteLoadBalancerTags`) et laissées de côté parce que #344 servait le
  chemin qui porte du trafic alors qu'une étiquette n'atteint aucun runtime ;
  `ReadVmsHealth` relève de l'**honnêteté**, et dit maintenant la chose la plus
  précise : non pas seulement que `--vm off` ne sonde rien, mais que `incus
  network load-balancer` ne rapporte aucune santé par backend *même sous OVN, où
  les connexions sont réellement distribuées*, de sorte que tout verdict serait
  inventé ; et les certificats serveur relèvent du fait que **rien ici ne
  termine TLS**.

- **`osc/Client.DeregisterVmsInLoadBalancer` est décliné pour inaccessibilité et
  non par manque de demande** (#344), ce qui est un refus plus fort, et que ce
  dépôt n'avait pas mesuré. Le provider 1.1.3 est la seule version dont le code
  contient l'appel, sur le chemin de mise à jour du `backend_vm_ids` porté par
  le load balancer lui-même, et ce chemin ne peut pas s'exécuter : l'attribut
  est déclaré `schema.TypeList`
  (`resource_outscale_load_balancer.go:150`) tandis que la mise à jour le
  convertit en `*schema.Set` (`:726`). Mesuré contre cet émulateur le
  2026-08-21 : le plugin panique sur `interface conversion: interface {} is
  []interface {}, not *schema.Set` avant qu'une requête ne soit construite. Les
  providers 1.7.0 et 1.8.0 ont retiré l'appel purement et simplement. Le
  détachement d'un backend passe par `UnlinkLoadBalancerBackendMachines`, qui
  est servi : servir celui-ci reviendrait à servir une opération qu'aucun client
  ne peut atteindre.

- **Deux écouteurs ne peuvent plus partager un même port d'entrée**, sur
  `CreateLoadBalancer` comme sur `CreateLoadBalancerListeners` (#344). Le refus
  est porteur plutôt que cosmétique : deux écouteurs sur un port, ce sont deux
  écouteurs de runtime sur un port, ce que le balanceur ne sait pas construire ;
  les stocker laisserait donc l'API décrire un balanceur que le runtime a
  refusé. Sa formulation évite délibérément le mot que le vrai service emploie :
  le provider 1.1.3 réessaie pendant cinq minutes sur toute erreur contenant
  `DuplicateListener`, et la condition n'est jamais transitoire ici, si bien que
  reprendre ce mot transformerait un refus exact en une attente de cinq minutes.

- `internal/shape.IsMintedIdentifier` porte tout le « est-ce un identifiant
  qu'un cloud a frappé », UUID, adresse, `i-<hex>` d'Outscale, pour la raison qui
  avait fait exporter `IsUUID`. Le replay relie sur cette réponse et
  l'assainisseur refuse de publier sur elle : un assainisseur qui reconnaîtrait
  un identifiant de moins que le replay publierait exactement les valeurs dont le
  replay sait qu'elles en sont.

### Corrigé

- **`scw instance security-group list-default-rules` atteint un jeu de règles au
  lieu d'un 404, une règle publie `dest_ip_range`, et une NIC privée publie sa
  date de dernière modification** (#431, #432, #436). Trois formes qu'un vrai
  compte `fr-par` a répondues et qu'aucun document n'aurait pu trouver.

  `default` est un segment littéral du chemin que construit le SDK de Scaleway,
  pas un identifiant, et ce pack le lisait comme tel : le segment correspondait à
  `{id}`, ne trouvait aucun groupe et répondait 404 — si bien que
  `instance/v1/API.ListDefaultSecurityGroupRules` se lisait « décliné » dans le
  registre de couverture pendant qu'une route vivante répondait faux à la
  commande. Elle est désormais servie, avec les six blocages SMTP sortants que
  l'enregistrement a mesurés, aucun modifiable, et le vrai CLI la pilote dans la
  suite de conformité.

  `dest_ip_range` est sur le fil de chaque règle de groupe de sécurité et n'est
  déclaré **ni** par la description d'API publiée de Scaleway **ni** par leur
  propre SDK Go. Il est servi à `null`, ce que répond le cloud, sur les cinq
  opérations qui rendent une règle.

  Une NIC privée publiait une date de création et aucune `modification_date`,
  sur la création, la lecture et la liste.


- **Un répartiteur publie le nœud sur lequel il tourne, un backend publie les
  trois valeurs par défaut que le cloud remplit, et l'adresse d'une passerelle
  publique publie un reverse** (#434, #435). Quatre-vingt-dix des divergences
  qu'un enregistrement réel de `fr-par` a trouvées tenaient à cinq causes, et
  chacune est une forme qu'un client lit.

  Un backend créé sans `send_proxy_v2`, `ssl_bridging` ni `host` répondait
  `null` sur les trois là où le cloud répond `false`, `false` et `""` ; les
  trois champs apparaissent sur onze opérations, parce qu'un frontend contient
  un backend et une ACL contient un frontend. Un répartiteur publiait un tableau
  `instances` vide là où le cloud en publie un nœud — l'enregistrement a
  renversé l'argument qui le gardait vide, puisque le nœud du cloud porte
  lui-même `ip_address: ""` : ce qui était retenu était la forme, pas une
  adresse. L'adresse d'une passerelle répondait `reverse: null` là où le cloud
  répond toujours un nom. Et une liste d'adresses `lb` ou `vpc-gw` ne nommant
  aucun projet était réduite au projet par défaut de ce pack, si bien qu'un
  client ayant créé une adresse dans son propre projet recevait une page vide.

  Deux champs sont désormais **déclinés avec leur raison** plutôt que servis
  creux : la `version` d'une passerelle, qui est celle d'un logiciel que cet
  émulateur ne fait pas tourner, et les éléments de `bastion_allowed_ips`, dont
  les trois opérations d'écriture étaient déjà déclinées — un filtre qu'aucun
  client ne peut modifier et que rien n'applique n'est pas un filtre.

- **Un zéro que la sonde peut fermer n'est plus classé comme une décision sur
  laquelle personne ne peut agir** (#445). `feint coverage --evidence <registre>
  --gaps` classait un zéro en `declared` (« pas du travail : aucun chemin
  n'existe pour fermer ce zéro ») dès que le registre disait qu'aucun client
  n'avait piloté l'opération, sur les sept axes à la fois, en affichant à côté de
  chacun la raison `Route.Undriven` de la route.

  Cette raison est une phrase sur les clients : « `exo limits` lit la liste
  entière des quotas, donc la lecture par nom n'a aucun chemin client ». Elle
  explique un zéro sur `driven`, `dataplane`, `behaviour` et `negative`, qu'aucun
  échange synthétique ne peut déplacer. Elle n'explique rien sur `probed`, que la
  sonde gagne sans le moindre client ; rien sur `contract`, que gagne toute
  réponse validée, y compris celle de la sonde ; et rien sur `shape`, résolu hors
  ligne depuis le catalogue d'enregistrements, qu'aucun trafic ne déplace dans un
  sens ni dans l'autre.

  Chaque axe déclare désormais ce qui le gagne, et deux témoins indépendants
  tiennent cette déclaration : l'un pilote un échange marqué et un échange nu
  contre un émulateur vivant puis relit les axes, l'autre refuse une déclaration
  que le registre commité contredit. Le registre porte **16 opérations qu'aucun
  client n'a pilotées et qui ont gagné `probed`, 14 qui ont gagné `contract` et
  une qui a gagné `shape`** : la preuve, par l'artefact lui-même, que « aucun
  client ne l'atteint » n'est pas ce qui tient ces trois axes à zéro.

  Mesuré sur `coverage/evidence.json` tel qu'il est commité, sans qu'un seul axe
  bouge et sans qu'une opération entre dans la file ou en sorte : **38 lignes sur
  22 opérations cessent de dire que personne ne peut agir**, et 64 autres cessent
  de dire « le registre n'explique pas ». Une sixième nature, `unvalidated`,
  nomme ce que ces zéros sont : une réponse tenue à la description d'API du
  fournisseur, qui ne demande ni compte cloud ni binaire client. #429 est la
  mesure derrière : 31 opérations Scaleway ont gagné `contract` et 29 ont gagné
  `probed` grâce à un seul correctif de l'extraction du contrat, sans toucher un
  client ni une ligne de pack, et si elles avaient porté une raison `Undriven`,
  cette file aurait déclaré les soixante « pas du travail ».

  `classifyGap` rend désormais la raison sur laquelle il a classé, de sorte
  qu'une ligne `declared` ne peut plus afficher une phrase que le classificateur
  n'a pas employée.

- **Outscale est prouvé sur chaque opération qu'il sert : `shape` atteint 93 sur
  93** (#427). Les quatre dernières étaient la famille de l'appairage de Nets,
  déclarée hors d'atteinte deux fois — par #354 puis par ce lot — pour une raison
  qui n'a jamais tenu au code : un appairage demande deux Nets à soi, le quota
  est de cinq, et quatre étaient pris par l'infrastructure de production du
  compte. Un second compte, vide, a rendu ces quatre opérations triviales à
  enregistrer.

  L'enregistrement est replié dans `shapes/outscale.json` ; la transcription
  elle-même n'est **pas** commitée comme corpus, et #438 dit pourquoi : son rejeu
  part en cascade depuis un conflit `CreateNet` que personne n'a nommé, et écrire
  35 exemptions pour une cause non nommée est précisément ce que
  `corpus/accepted.json` existe pour empêcher.

- **Le `TaskId` d'un volume est décliné plutôt qu'inventé, et c'est la mesure qui
  le dit** (#427, #437). Un vrai compte Outscale rend `TaskId` sur un volume, et
  la lecture naïve en conclut « le pack omet un champ ». Non : sur les huit
  enregistrements de volume que porte la recording, **sept ne portent aucun
  `TaskId`**, et celui qui en porte un est le volume en cours de
  redimensionnement. C'est une propriété d'un volume qui *a* une tâche, et cet
  émulateur n'en a aucune : un redimensionnement s'y achève dans l'appel. La
  leçon `Iops` de #389 une seconde fois : un catalogue de formes est l'union de
  tous les champs jamais observés, et lire une union comme une exigence par
  enregistrement, c'est ainsi qu'une valeur par défaut finit servie à tous.

- **Un frontend de répartiteur rend `certificate`, et il vaut null** (#427). Le
  vrai cloud porte le singulier déprécié à côté de `certificate_ids` sur chaque
  frontend et sur le frontend qu'une ACL embarque ; cet émulateur omettait la
  clé. Invisible pour un client qui décode dans une structure, visible pour un
  client qui compare des ensembles de champs — et c'est l'enregistrement d'un
  vrai LB-S qui a transformé « nous ne servons pas de certificats » d'un silence
  en une réponse énoncée. Null est la seule valeur que cet émulateur pourrait y
  porter, et c'est la valeur observée.

  Trouvé par la garde d'omission d'une passe de conformance, sur huit opérations
  d'un coup, dès les nouvelles formes commitées. La garde a fait exactement ce
  pour quoi elle existe.

- **L'offre de passerelle était validée contre une liste dont personne ne se
  portait garant, et un enregistrement a prouvé que cela coûtait 143 constats de
  rejeu** (#427). Une transcription assainie remplace toute valeur qu'un pack ne
  publie pas comme sienne : une liste fermée contre laquelle le pack répond
  `400` sans s'en porter garant rend donc son propre enregistrement
  irrejouable. Le `CreateGateway` enregistré a été refusé, et chaque lecture
  suivante s'adressait à une passerelle jamais créée — 126 des constats étaient
  `GetGateway` « omettant » les champs d'un objet inexistant.

  `PublicVocabulary` lit désormais `gatewayTypes` à côté de `knownZones` et
  `knownRegions`, depuis la table et non depuis une copie. Son commentaire
  affirmait le contraire, mot pour mot : « un type commercial … cet émulateur ne
  valide pas une requête contre ». `createGateway` en valide un.
  `TestTheVocabularyVouchesForEveryListThePackValidatesAgainst` est écrit sur les
  tables, de sorte qu'une offre nouvelle ne peut pas le faire passer pendant que
  le vocabulaire dérive, et quatre mutations de
  `tools/falsify/specs/vocabulary-covers-what-it-validates.json` mordent.

- **Les deux créations `block/v1alpha1` rendent le statut que le vrai cloud
  rend** (#427). `CreateVolume` et `CreateSnapshot` rendaient `201` ; les deux
  rendent `200` sur un vrai compte `fr-par`, mesuré sur le fil le 2026-08-24.
  Troisième produit mesuré ainsi après `vpc/v2` et `ipam/v1`, et chacun n'affirme
  que pour le produit dont la réponse a été vue.

- **Dix des points de l'axe `shape` étaient gagnés par un corps vide, et six
  d'entre eux précèdent ce lot** (#427). Un `204` ne porte aucun corps : le
  parcours des champs décodait `nil` et écrivait une entrée au chemin vide, de
  type `null`. Cette entrée ne nomme aucun champ et n'énonce le type de rien,
  mais elle rend `len(Fields)` non nul — et deux consommateurs se branchent
  exactement là-dessus : l'axe `shape` compte l'opération comme *observée*, et
  `feint shapes --check` la traite comme ayant une forme à comparer.

  Mesuré sur le `shapes/scaleway.json` commité : six opérations la portaient,
  toutes des `DELETE`. L'axe publiait donc 134 là où 128 avaient été observées.
  Le compte bougeait dans le sens qui ressemble à un progrès, et c'est ce qui
  l'a rendu invisible.

  Un catalogue ne retient plus aucun champ à la racine, à l'entrée comme à la
  sortie — la seconde moitié parce qu'un fichier commité avant la règle ne doit
  pas continuer d'être cru. `tools/falsify/specs/root-path-is-not-a-field.json`
  remet un champ fantôme de chaque côté, et les deux mutations mordent.

- **Deux créations Scaleway rendent le statut que le vrai cloud rend, et non
  celui que le pack supposait** (#427). `vpc/v2/API.CreateRoute` et
  `ipam/v1/API.BookIP` rendaient `201` parce que toutes les autres créations du
  pack le font ; les deux rendent `200` sur un vrai compte `fr-par`, mesuré sur
  le fil le 2026-08-24 et enregistré dans
  `corpus/scaleway/scw-free-shapes.jsonl`.

  Ni `scw` ni le fournisseur Terraform ne l'auraient jamais signalé : tous deux
  acceptent n'importe quel `2xx` et n'affichent aucun statut. C'est exactement
  ainsi qu'une erreur pareille survit indéfiniment. `CreateRoute` était même
  nommée dans un commentaire de test comme la création vpc/v2 *non mesurée*, qui
  gardait donc le `201` du pack : l'exception est levée par la mesure qu'elle
  réclamait.

- **L'axe `behaviour` était une fonction de l'ordonnanceur, et deux exécutions
  identiques s'accordaient sur le total en désaccord sur six opérations**
  (#398). Deux `mise run conformance` du même commit, machines éteintes, ont
  marqué **311** opérations chacune sans marquer les mêmes :
  `block/v1/API.CreateVolume`, `osc/Client.DeleteSecurityGroupRule` et
  `osc/Client.UnlinkLoadBalancerBackendMachines` dans la première,
  `instance/v2alpha1/API.DeletePrivateNetworkInterface`,
  `instance/v2alpha1/API.GetPlacementGroup` et `osc/Client.UnlinkVolume` dans la
  seconde. L'égalité des totaux est le piège, et c'est pourquoi le correctif se
  mesure sur les ensembles : le critère d'acceptation de l'issue, le même chiffre
  deux fois, passe sur le code défectueux.

  Une touche du store n'était attribuée que tant qu'une seule requête non sonde
  était en vol dans tout le processus, et terraform tourne en `-parallelism=10`
  sous un span qui couvre tout son cycle de vie. Le store répond déjà à la
  question que cette règle approximait : `Observe` exécute son rappel de façon
  synchrone, hors du verrou du store, **sur la goroutine qui a fait la touche**.
  C'est ce qui est lu maintenant, et cela met fin à une surestimation que
  personne n'avait vue : une touche faite par la goroutine de la sonde, ou par un
  handler que `serveFault` appelle directement et qui n'entre jamais dans
  l'ensemble en vol, était créditée à n'importe quelle requête cliente qui se
  trouvait en vol à côté.

  Mesuré après le changement, même machine, même commit, machines éteintes :
  **316 et 316, et les mêmes 316**. Le registre entier, sept axes et 370
  opérations, est désormais identique entre deux exécutions, là où six entrées
  `behaviour` exactement différaient avant, et rien d'autre. Cinq opérations
  récupérées, aucune perdue, et chaque span de chaque suite a rapporté zéro
  touche non attribuée. Ce qui reste inattribuable est borné et dit plutôt que
  jeté : une requête déjà en vol quand
  un span s'ouvre ne porte pas d'identité, la fermeture du span publie combien de
  touches cela a coûté, et `tools/conformance/prove.sh` l'affiche.

  Et l'autre moitié de la même issue : `runtimesLost` refusait une régénération
  qui atteint moins de *runtimes*, et rien ne regardait les opérations, de sorte
  qu'un registre pouvait rétrograder une opération dont l'assertion était encore
  dans la suite et passait encore, sans un mot. `feint evidence` nomme désormais,
  axe par axe, chaque opération que le registre remplacé avait gagnée et que
  cette exécution n'atteint pas. Un rapport et non un refus : un axe peut
  légitimement rétrécir quand une affirmation est corrigée, et une suite qui perd
  une assertion *doit* rétrograder ce qu'elle prouvait, ce qui est la
  falsification sous laquelle ce registre vit.

- **Trois pourcentages d'axe publiés dans la documentation étaient faux, et rien
  dans le dépôt ne refusait un chiffre mesuré écrit à la main** (#406).
  `docs/proxy.md`, `docs/conformance.md`, `corpus/README.md`, les deux CHANGELOG
  et le tableau d'ouverture de #390 affirmaient que six des sept axes de preuve
  se tenaient dans une plage de pourcentages. Mesuré avec `feint coverage
  --evidence coverage/evidence.json` sur le même artefact, trois des six en
  sortaient, et l'un d'un facteur six. La cause est la règle 2 du skill
  measurement-integrity appliquée aux chiffres phares du projet : ils venaient
  d'un script jetable qui lisait chaque axe comme un booléen, `if o.get(axe)`,
  alors que trois des sept sont des verdicts. `"unobserved"` est une chaîne non
  vide, donc toute opération dont la forme n'avait jamais été comparée à une
  réponse de vrai cloud comptait comme une opération qui l'avait été.

  Tous sont corrigés, et `docs/proxy.md` porte une note qui dit ce qu'il
  affirmait, parce qu'un chiffre édité en silence n'apprend rien. La récidive est
  l'objet du changement : **un pourcentage d'axe vit dans un bloc généré ou nulle
  part**, et `feint docs --check`, que lancent prepush et le crochet pre-commit,
  refuse un pourcentage posé à côté d'un nom d'axe hors d'un bloc, dans tout
  Markdown du dépôt. Les décomptes sont laissés tels quels : « 35 sur 370 » est
  ce dont une file de travail est faite.

  **Le même défaut a été trouvé dans le lecteur Go du dépôt pendant que la
  correction était testée**, et c'est la partie qui mérite d'être gardée.
  `probed` était gagné par `e.Probed != "none"`, donc une ligne ne portant aucun
  verdict, la chaîne vide qu'`encoding/json` laisse pour une clé absente, gagnait
  l'axe, exactement comme `if o.get(axe)` le faisait dans le script. Il est
  nommé positivement maintenant, et `readEvidence` refuse un registre dont
  `probed`, `contract` ou `shape` sort de son propre vocabulaire : le commentaire
  de cette fonction affirmait déjà qu'elle « refuse ce dont elle ne peut rendre
  compte » et ne le faisait pas, ce qui est le défaut récurrent le plus coûteux
  de ce dépôt rencontré une fois de plus.

- **La suite de conformance orphelinait un de ses propres réseaux en cours
  d'exécution, et cette seule course de démantèlement est ce dont #316, #342 et
  #375 étaient tous en aval** (#386). `mise run evidence:update` a échoué deux
  fois en mode pont le 2026-08-21, chaque fois sur un sous-réseau de
  `examples/stacks/outscale/main.tf`, et chaque échec était précédé dans le
  journal de l'émulateur par `detach isolation from fnt-… : open
  /var/lib/incus/networks/…/dnsmasq.raw: no such file or directory`. C'est la
  réconciliation d'isolation d'une requête qui atteint un réseau qu'une autre
  requête a déjà supprimé : la passe liste le store, une suppression concurrente
  retire un des membres qu'elle avait listés, et l'édition de configuration
  arrive sur un réseau que le démon ne connaît plus. L'objet meurt, l'interface
  et son `dnsmasq` lui survivent, et l'exécution suivante qui veut ce bloc meurt
  au bout de plusieurs minutes sur « Address already in use ». **Les trois
  issues précédentes ont rendu le résidu visible ou survivable ; aucune n'a
  traité ce qui le produit**, et aucun contrôle sur le pas de la porte ne le
  peut, puisque l'hôte est propre au démarrage et que c'est l'exécution
  elle-même qui le salit à la douzième étape.

  **Deux moitiés, parce que l'une sans l'autre laisse la course ouverte.** Le
  pilote Incus prend désormais `serialise.Lock("incus.network." + name)` dans
  `EnsureNetwork`, `IsolateNetwork` et `RemoveNetwork` : aucune édition de
  configuration d'un réseau n'est en vol pendant que sa suppression tourne. Par
  réseau et jamais global, car un verrou global mettrait tous les sous-réseaux
  d'une pile en file derrière une seule suppression, c'est-à-dire l'erreur que
  `internal/core/machine/serialise.go` consigne déjà avoir commise une fois,
  avec l'allocation d'interface (#348). Et `IsolateNetwork`, sous ce verrou,
  demande au démon si le réseau est encore là avant d'éditer quoi que ce soit :
  le verrou seul laisse encore passer une suppression qui l'a gagné, et la
  question seule est un temps de contrôle qu'une suppression traverse.

  **Un détachement qui n'a pas pu avoir lieu est rapporté, jamais compté comme
  fait.** Le pilote rend `machine.ErrNetworkGone` et `ReconcileIsolation` le
  journalise en nommant le réseau. En avertissement plutôt qu'en erreur : aucun
  jeu de règles n'était nécessaire et aucun ne manque, et une ligne qui se
  déclenche à chaque destruction parallèle est la façon dont un journal cesse
  d'être une preuve. Le jeu de règles qui isolait le réseau est maintenant
  retiré par la suppression elle-même, puisque la passe qui le retirait est
  celle qui refuse désormais de tourner contre un réseau disparu.

  Reproduite délibérément avant toute correction. Le faux runtime modélise le
  comportement du démon que le journal a montré (une édition réapplique le
  réseau, ce qui relève son pont et son `dnsmasq`, avant d'ouvrir
  `dnsmasq.raw`), de sorte qu'une édition qui croise une suppression laisse un
  service debout pour un réseau disparu, exactement le résidu mesuré par #342.
  Falsifiée dans `tools/falsify/specs/teardown-race.json` : cinq mutations,
  toutes rouges, et la mutation du verrou mesurée hors dépôt à 10/10 rouge sans
  le verrou et 10/10 vert avec.

- **Exoscale répond 409 pour une clé publique qu'il ne sait pas lire, et ce pack
  aussi** (#390). `POST /v2/ssh-key` portant une chaîne qui n'est pas une clé
  OpenSSH répondait 400 ici et `409 {"message":"Public key is invalid"}` sur un
  vrai compte `ch-gva-2`, mesuré le 2026-08-21. Les deux refusent ; un client se
  branche sur lequel, et la règle 4 dit que c'est le fournisseur qui tranche.

- **Une clé publique OpenSSH dont la matière nomme un autre algorithme est
  refusée** (#390). `ssh-ed25519 AAAA` passait : le premier champ est un
  algorithme connu et le second est du base64 valide, ce qui était tout ce que
  `sshkey.Parse` contrôlait. La matière nomme son propre algorithme (RFC 4253) et
  ce doit être celui que la ligne déclare : le vrai cloud fait exactement ce
  contrôle et répond 400 `invalid key type`, mesuré contre un compte réel. Bien
  formé n'est pas valide, et deux fixtures de ce dépôt s'appuyaient sur ce trou.
  Chaque pack qui accepte une clé publique gagne le refus.

- **Lister les routes d'un frontend d'équilibreur qui n'existe pas répond 404 au
  lieu d'une page vide** (#390). `scw lb route list frontend-id=<absent>`
  répondait 200 avec `routes: []`, ce qu'un client lit comme « ce frontend ne
  porte aucune route » et non comme « ce frontend n'existe pas ». Le cloud répond
  404 `frontend not Found`.

- **Un jeu d'options DHCP Outscale nommant un serveur qui n'est pas une adresse
  IPv4 est refusé** (#390). `CreateDhcpOptions` stockait la chaîne et répondait
  200 ; le cloud répond 400 `InvalidParameterValue` avant de rien stocker. Le
  mot-clé `OutscaleProvidedDNS` de la plateforme reste accepté, étant la seule
  valeur de ce champ qui ne soit pas une adresse.

- **L'assainisseur ne publie plus une adresse hors de son propre espace
  synthétique** (#390). Un bloc plus court que `198.18.0.0/15` lui-même ne peut
  pas y être placé, et masquer le remplacement à cette longueur en sortait
  aussitôt : `10.0.0.0/8` revenait en `198.0.0.0/8`, dont la moitié adresse
  appartient à quelqu'un. C'est la longueur de préfixe qu'une API valide, donc
  elle survit, et la moitié adresse devient celle de l'espace.

- **Trois champs que la vraie API Exoscale répond et que cet émulateur omettait,
  tous invisibles à tout contrôle qui lit un document** (#370, #371). Ils font
  105 des 192 divergences rapportées par l'enregistrement du 2026-08-21 d'un
  vrai compte `ch-gva-2`, et `corpus/accepted.json` retombe à 87 en conséquence.

  `GET /v2/zone` répond maintenant un `id` par zone, dérivé du nom de la zone :
  deux lectures d'un même émulateur s'accordent, et un émulateur redémarré
  s'accorde encore avec lui-même. #370 dit pourquoi cette moitié compte : un
  identifiant qui bougerait d'une lecture à l'autre serait pire que pas
  d'identifiant du tout, puisqu'un client qui l'a stocké détiendrait une valeur
  qui ne nomme rien. À lui seul, c'est 51 constats, parce que `exo` liste les
  zones avant presque chaque commande. `GET /v2/security-group` répond
  maintenant `visibility` sur chaque groupe, sur la liste comme sur la lecture
  d'un groupe (46). Et une règle qui pointe vers un autre groupe publie
  maintenant le `name` de ce groupe à côté de son `id` (8) : c'est la forme
  qu'`examples/stacks/exoscale/main.tf` écrit sous `user_security_group_id`,
  « la couche applicative accepte la couche web et personne d'autre ».
  **#371 affirme qu'un consommateur qui lit le nom sur la règle ne voit rien, et
  cette moitié-là ne survit pas à la mesure** : `exo compute security-group
  show` affiche `SG:web` avec le nom comme sans lui, parce qu'il résout la
  référence par son id contre les groupes qu'il a déjà listés. La raison de le
  servir est la seule dont ce projet ait jamais besoin, à savoir que
  l'enregistrement dit que le cloud envoie un nom, et non un symptôme client que
  personne n'a reproduit.

  **Pourquoi rien ne les avait vus, et c'est la part qu'il faut garder.** Deux
  des trois ne sont pas non plus dans le `source.yaml` publié par Exoscale :
  le contrat, la porte des formes, la sonde et le pack étaient donc d'accord
  entre eux, et tous les quatre avaient tort de la même façon. L'id de zone
  avait même été levé une fois, sous #94, et clos en non-défaut au motif que le
  servir faisait échouer le contrôle de contrat de cet émulateur. C'était vrai,
  et c'était le mauvais bout à corriger. Seul un enregistrement du fil pouvait
  les contredire, et il en existe un. Le troisième n'a demandé aucun changement
  de contrat : le document déclarait déjà le champ, seul le pack l'avait laissé
  tomber.

- **Un service DHCP laissé derrière lui par une exécution faisait échouer la
  jambe runtime à chaque fois, et le seul remède était un humain qui lise le
  journal** (#375). Le balayage de `tools/conformance/*/network.sh` trouvait le
  résidu, le nommait exactement et imprimait `sudo kill <pid>`, puis sortait en
  1 : le processus appartient à l'utilisateur `incus`, et l'opérateur qui lance
  la suite n'a pas le droit de le signaler. Le diagnostic était juste et
  personne n'exécutait le remède, donc la jambe runtime de
  `mise run evidence:update` est morte au même endroit trois fois de suite,
  chaque fois *après* que toutes les suites clientes aient tourné. **Une porte
  dont le seul remède est un geste manuel que quelqu'un doit remarquer est une
  porte qu'on finit par contourner**, et c'est exactement comme on apprend
  `--no-verify`.

  Deux changements, et aucun des deux ne monte en privilèges. `feint clean
  --check` pose la question du balayage sans rien faire : il nomme ce qu'une
  exécution précédente a laissé sur un bloc d'adresses, sépare ce que cet
  utilisateur peut terminer de ce qu'il ne peut pas, et ne sort en 1 que pour le
  second cas. La sonde de permission derrière lui est le signal 0 : le noyau
  fait le contrôle qu'il ferait pour un vrai signal et ne délivre rien, seule
  forme acceptable pour une question dont le sujet est un processus que
  personne ici n'a démarré. Et `guard_leftovers`
  (`tools/conformance/guard.sh`) met cette question sur le pas de la porte : les
  trois suites réseau et `mise run conformance` lui-même la posent avant que
  quoi que ce soit ne démarre, si bien que la réponse arrive à la seconde zéro
  au lieu de douze étapes plus loin. Mesuré sur la reproduction : **1 seconde
  pour refuser, en nommant le pid**, là où le même hôte passait d'abord par
  toute la série des clients.

  **Ce que ce n'est pas, c'est une suite qui monte en privilèges.** Une
  exécution de conformance qui s'arrogerait le droit de terminer un démon
  qu'elle n'a pas démarré serait un défaut pire que celui qu'elle contourne :
  c'est la question que `mustOwn` pose au pilote, un étage plus haut.
  L'élévation appartient à l'opérateur, en une ligne plutôt qu'un pid à
  retaper : `sudo feint clean --vm <mode>`, le même balayage, qui repose chaque
  question de propriété au moment du signal. Le défaut a été reproduit
  délibérément avant toute correction, et falsifié dans
  `tools/falsify/specs/unkillable-dhcp-orphan.json`.

  **Une prémisse ne survit pas à la mesure.** Ces résidus avaient été mesurés
  comme des restes d'une exécution *précédente* (#316, #342) ; la jambe
  machines-on en a produit un elle-même le 21 août 2026, en cours d'exécution et
  en mode pont, à partir d'un réseau qu'elle venait de créer : le runtime
  listait ce réseau comme non géré alors que son pont et son `dnsmasq` étaient
  toujours là. Les messages disent donc « une exécution » et non « une exécution
  précédente », parce qu'envoyer l'opérateur chercher une exécution passée
  l'envoie au mauvais endroit, et parce qu'aucune porte n'empêche ce que
  l'exécution crée elle-même. C'est déterministe : la jambe a été lancée deux
  fois et a échoué les deux fois, dans la même suite, sur un sous-réseau
  d'`examples/stacks/outscale/main.tf`, chaque échec précédé d'un détachement de
  l'isolation arrivant sur un réseau dont la suppression avait déjà retiré le
  répertoire d'état. `docs/limits.md` porte les lignes de journal. Cette
  naissance est un défaut distinct, non corrigé ici.

- **La porte du corpus retenait son avertissement « le cloud a bougé »
  précisément quand il servait le plus.** Les deux avertissements — celui de
  l'enregistrement vieilli et celui de la bougeotte mesurée que #359 réécrit —
  étaient émis sous la garde des invariants non exercés (#343) et sous celle des
  exemptions périmées : une exécution rouge pour l'une de ces deux raisons ne
  disait pas un mot d'un enregistrement sous lequel le fournisseur a été mesuré
  bouger. C'est le pire moment pour le taire : *ré-enregistrer ce fichier* est
  un correctif candidat au rouge qu'on est en train de rapporter, et qui ne le
  voit pas cherche un défaut dans l'émulateur. Aucun des deux ne lit autre chose
  que le fichier d'acceptation, aucun ne déplace un code de sortie — leur propre
  commentaire l'énonce comme leur contrat ; un placement tardif ne pouvait donc
  que les retenir. `warnMovedCorpora` affirmait « à chaque exécution » sans que
  rien ne le rende vrai :
  `TestTheMovedWarningSurvivesARunThatIsRedForAnotherReason` le rend vrai, sur
  l'exécution la plus pauvre qui atteigne l'impression.
- **Cinq formes que le pack Outscale rendait autrement que le cloud**, trouvées
  chacune en rejouant l'enregistrement d'un vrai compte, et retirant chacune son
  exemption de `corpus/accepted.json` (#378, #379, #381, #382, #383). Le nombre
  de divergences acceptées est passé de 289 à 141.

  - **Une machine rend `UserData` et `Tags`** (#378). Le cloud écrit les deux
    sur chaque machine (`""` et `[]` sur une machine créée sans ni l'un ni
    l'autre) et ce pack ne les écrivait que lorsqu'il avait quelque chose à y
    mettre. Toutes les autres familles du pack posaient déjà `"Tags": []` à la
    création ; la Vm était la seule qui ne le faisait pas.
  - **Deux listes reviennent dans l'ordre où l'API les rend** (#379). Les
    groupes de sécurité d'une machine sont triés par `SecurityGroupId`
    croissant, et les routes d'une table par destination **en lecture**, alors
    que la création les rend dans l'ordre d'ajout. Les deux sont mesurés, et le
    second est le cloud en désaccord avec lui-même : un client qui stocke la
    réponse de la création puis relit voit les deux permuter, ce qu'un émulateur
    qui « rangerait » masquerait. Terraform stocke les deux comme des listes,
    donc un ordre propre à cet émulateur est une divergence de plan qui ne
    converge jamais : #320, un fournisseur plus loin.
  - **`DeleteRoute` et `DeleteLoadBalancer` rendent l'objet** (#381) plutôt que
    la seule enveloppe, ce qui permet à un client de rafraîchir son état en un
    appel au lieu de deux.
  - **Une règle qui nomme un autre groupe de sécurité publie l'`AccountId` et le
    nom de ce groupe** (#382). La forme `Rules[].SecurityGroupsMembers[]`
    recopiait le membre du client tel quel, si bien que la réponse en disait
    moins que celle du cloud sur un groupe nommé par son seul identifiant, et
    c'est `AccountId` qui distingue un groupe du compte d'un groupe partagé par
    un tiers.
  - **Une image porte le mappage de son périphérique racine et ses permissions
    de lancement** (#383) : le nom du périphérique, la taille et le type du
    volume racine, que lit un client avant de dimensionner le volume qu'il crée.
    Le `SnapshotId` et l'`Iops` de ce mappage sont **déclarés** plutôt que
    servis, et la limite est celle que le code traçait déjà : nommer un
    instantané auquel `ReadSnapshots` ne peut pas répondre, c'est ainsi que la
    résolution d'un client échoue sur un objet inexistant, et un volume
    `standard` n'a pas d'IOPS provisionnées.

  **Un ordre n'est délibérément pas déclaré en `ReplayInvariant`, et c'est une
  limite qui mérite d'être écrite.** L'ordre des groupes de sécurité d'une
  machine dérive d'identifiants frappés par le *cloud* ; aucun émulateur ne
  frappe les mêmes, et `feint replay` compare position par position après
  reliaison. Le déclarer n'achèterait donc qu'une exemption permanente, et une
  exemption permanente est une barrière qui a cessé sans bruit de couvrir ce
  qu'elle nomme. Un test unitaire du pack le tient à la place. Ce qui est
  déclaré, c'est l'ordre des routes, que le cloud dérive d'une valeur que les
  deux côtés portent.

- **L'enregistreur n'écrit plus deux valeurs différentes comme une seule**
  (#384). Une expurgation remplaçait toute valeur reconnue par la même chaîne,
  ce qui est juste pour un identifiant secret et faux pour tout le reste que
  capture une règle portant sur les *noms*. Mesuré en enregistrant un vrai
  compte Outscale : `KeypairName` contient `key`, donc le nom de la clé importée
  et celui, inventé, d'un refus délibéré sont tous deux arrivés dans la
  transcription en `REDACTED`, et le fichier affirmait que les deux appels
  visaient le même objet. Au rejeu, l'émulateur supprimait la vraie clé sur
  l'échange censé répondre 404 et n'avait plus rien pour celui censé répondre
  200.

  Un remplaçant porte désormais un suffixe par valeur : `REDACTED-<8 hex>`, un
  HMAC sous une clé tirée une fois par processus et jamais écrite. Trois
  propriétés, et la troisième explique pourquoi c'est un HMAC et non un
  condensat : une même valeur garde le même remplaçant dans tout
  l'enregistrement, deux valeurs en obtiennent deux, et **la valeur reste
  irrécupérable depuis le remplaçant quelle que soit son entropie**, là où un
  condensat simple d'un secret court se retrouve en essayant des candidats.
  `internal/corpus` les renumérote en `REDACTED-<n>` pour que l'artefact
  commité porte le compteur du sanitiseur et que l'alphabet n'ait qu'une seule
  graphie à admettre.

  Rien n'a bougé sur *quels* noms sont expurgés : `carriers` est intact, la
  liste blanche des en-têtes est intacte, et la seule valeur rachetée par son
  propre format (une ligne de clé publique OpenSSH) l'est toujours.
  `internal/corpus` refusait déjà exactement cette forme une étape plus loin
  (« la transcription dirait alors que deux objets du compte n'en font qu'un »)
  et ne pouvait pas la voir, la fusion ayant lieu dans l'enregistreur avant que
  le sanitiseur ne rencontre deux valeurs. Six mutations falsifiées dans
  `tools/falsify/specs/distinct-placeholders.json`.

- **Un subnet ne tombe plus hors du net qui le contient** (#354). Le sanitiseur
  frappait chaque CIDR depuis un compteur unique : un Net enregistré en
  `10.111.0.0/16` et son Subnet en `10.111.1.0/24` ressortaient en deux blocs
  disjoints, et l'émulateur répondait `400 IpRange … is outside the Net range …`
  là où le cloud répondait 200, emportant avec lui la machine, le volume, la
  NIC, l'IP publique, la liaison de table de routage, le service NAT et le load
  balancer situés derrière ce subnet : une centaine de constats, dont aucun
  n'était un défaut de l'émulateur. `mint.planBlocks` décide désormais tous les
  blocs d'un enregistrement en une passe, du préfixe le plus court au plus long,
  et place un enfant au décalage qu'il occupait dans son parent.

  **C'est le troisième défaut d'une même famille, et la famille est la leçon.**
  Un masque qui cessait d'être un masque, une plage d'adresses qui courait à
  l'envers, et maintenant un subnet hors de son net : chacun était une *relation
  entre* valeurs plutôt qu'une propriété de l'une d'elles, c'est-à-dire
  précisément ce qu'un parcours valeur par valeur ne peut pas voir. D'où une
  passe préalable à côté de `learnAddressOrder`, et non un quatrième cas
  particulier. `TestASubnetStaysInsideItsNet`.

- **Un corpus est rejoué contre un émulateur servant la région d'où il a été
  enregistré** (#354). `corpus/accepted.json` porte désormais une `region` (et
  une `zone`) par enregistrement, et `feint corpus --check` construit les packs
  de chaque fichier à partir d'elle. Chez Outscale et Exoscale, une région n'est
  pas une propriété de la surface d'API mais de l'endpoint vers lequel le client
  pointe, et un pack refuse une création nommant une zone que son déploiement ne
  publie pas : l'invariant #269. Un enregistrement `cloudgouv-eu-west-1` rejoué
  contre un émulateur `eu-west-2` était donc refusé dès son propre
  `CreateSubnet`, et sur tout ce qui en dépendait.

  Lue depuis le manifeste versionné et jamais depuis l'environnement, ce qui
  fait du verdict de cette barrière une propriété de fichiers commités plutôt
  que du runner : l'affirmation que porte
  `TestTheGatesVerdictDoesNotDependOnTheEnvironment` devient vraie par
  construction au lieu de l'être par coïncidence.
  `TestACorpusIsReplayedInTheRegionItNames` tient les deux moitiés : région
  nommée, l'enregistrement rejoue propre ; région absente, le même
  enregistrement est refusé.

- **Quatre défauts du sanitiseur, tous trouvés en enregistrant un deuxième
  fournisseur, et tous du genre qui fabrique une divergence** (#354). À eux
  quatre ils ont caché tout le cycle de vie du réseau privé Exoscale derrière
  une vingtaine de constats, dont aucun n'était un défaut de l'émulateur. Chacun
  est falsifié dans `tools/falsify/specs/sanitised-corpus.json`.

  **`0.0.0.0/0` ne pouvait pas être écrit du tout.** Masquer un préfixe de
  longueur nulle rend le même préfixe, donc le mint rendait la valeur inchangée,
  le recoupement avec l'enregistrement trouvait la même chaîne des deux côtés,
  et `--sanitise` refusait tout le run sans rien écrire, sur un enregistrement
  dont le seul tort était une règle de groupe de sécurité ouvrant un port sur
  Internet. Il existe un tel préfixe par famille et il sélectionne toutes les
  adresses : il survit désormais verbatim, faute d'un remplacement qui soit à la
  fois de la même forme et d'une autre valeur.
  `TestTheDefaultRouteSurvivesSanitisation`.

  **Un masque en notation pointée passait par le mint d'adresses.**
  `255.255.255.0` en ressortait adresse d'hôte de l'espace synthétique,
  `exoscale/v2.create-private-network` répondait `400 netmask is not a usable
  IPv4 netmask` là où le cloud répondait 200, et la lecture, la modification, la
  suppression et trois sondages d'opération derrière elle répondaient 404 pour
  cette seule raison. Un masque est maintenant remplacé par un masque, via une
  bijection de 1..32 sur lui-même sans point fixe : le masque du compte ne
  survit jamais et la valeur écrite est toujours un masque.
  `TestANetmaskIsReplacedByANetmask`.

  **Une plage d'adresses ressortait à l'envers.** `start-ip` et `end-ip` étaient
  frappés dans l'ordre où le parcours les rencontrait, c'est-à-dire l'ordre
  alphabétique, donc l'artefact portait `end` en dessous de `start` et la même
  création répondait `400 end-ip is below start-ip`. Les adresses sont
  désormais rangées en triant celles de l'enregistrement avant que rien ne soit
  écrit, de sorte que les synthétiques se trient comme les originales. La règle
  ne nomme aucun champ, parce que `start`/`end`, `first`/`last` et la façon dont
  un quatrième fournisseur les appellera sont un seul problème.
  `TestAnAddressRangeStillRunsForwards`.

  **Une adresse synthétique pouvait être frappée hors de l'espace synthétique.**
  Le `ReadPublicIpRanges` d'Outscale publie tout l'espace d'adressage public du
  fournisseur, 90 blocs le 2026-08-21 dont trois /20 parmi 79 /24, et le
  compteur qui atteignait les /20 était décalé de douze bits et atterrissait en
  198.20.0.0, hors du seul bloc IPv4 qu'un transcript assaini peut porter.
  L'alphabet a refusé l'artefact, ce qui était la bonne issue et le mauvais
  message : la faute était arithmétique, quatre fonctions plus loin. `offsetV4`
  confine désormais ce qu'on lui donne, si bien qu'un espace sans place restante
  répète un remplacement et que `Sanitise` refuse *cela* en le nommant. Un
  corpus où deux blocs d'un compte se lisent comme un seul est le constat que
  #270 a fait à la main, et il ne doit jamais être fabriqué ici.
  `TestASyntheticAddressStaysInTheSyntheticSpace` et
  `TestASpaceWithNoRoomLeftIsRefusedRatherThanOverrun`.

- **Le scan du corpus committé lisait chaque fichier contre le contrat
  Scaleway** (#354). L'alphabet qu'un transcript assaini peut porter inclut les
  valeurs que la description du fournisseur énumère, et les noms de zones et
  familles de machines d'Exoscale ne sont que dans le document d'Exoscale : lu
  contre celui de Scaleway, le premier corpus Exoscale committé rapportait des
  centaines de fuites qui n'en étaient pas. Le contrat est désormais celui que
  nomme le répertoire du fichier. C'était vrai tant que Scaleway était le seul
  corpus, et c'est devenu un verdict faux le jour où un deuxième est arrivé, ce
  qui est la forme de tout gate qui cesse de mesurer en silence.

- **Les huit divergences relevées par le premier corpus Scaleway réel ont
  disparu, et `corpus/accepted.json` porte une liste d'exemptions vide** (#355).
  Le gate est entré en les portant, chacune avec #355 écrit à côté ; la règle
  d'obsolescence a rendu leur suppression obligatoire le jour où l'émulateur a
  cessé de les produire. Trois causes, et dire laquelle était le travail : un
  défaut, une limite déclarée, ou l'instrument qui ment sur lui-même.

  **Trois défauts de l'émulateur, dont deux étaient invisibles tant que
  l'instrument n'était pas réparé.** Le VPC par défaut ne répondait aucune
  étiquette là où le vrai répond `tags: ["default"]`, mesuré deux fois le
  2026-08-21, par `scw vpc vpc list` contre un vrai compte fr-par et par
  l'enregistrement lui-même. `iam/v1alpha1/API.CreateSSHKey` répondait **201
  là où le fil portait 200**, la même famille que les deux créations `vpc/v2`
  trouvées à la main par #270, cachée tant que le cycle de vie de la clé ne
  pouvait pas être rejoué. Et une clé SSH était publiée **avec le commentaire
  envoyé par le client**, alors que le cloud le retire : une clé créée sur un
  vrai compte sous la forme `ssh-ed25519 <matériel> feint-corpus-echo` (98
  octets, trois champs) se relit `ssh-ed25519 <matériel>` (80 octets, deux
  champs), et le corpus réenregistré porte le même fait vu de l'autre côté :
  le corps de la requête et la réponse tiennent deux chaînes *différentes* sur
  `public_key`. L'empreinte ne bouge pas, étant calculée sur le blob décodé et
  non sur la ligne. Aucun autre contrôle d'ici ne voit ces trois défauts :
  personne ne lit l'étiquette côté client, `scw` accepte n'importe quel 2xx sans
  en montrer aucun, et un contrat déclare le *type* de `public_key` et non ce
  que le cloud y met. Ce dernier point est une **valeur**, que le gate de corpus
  consigne sans la noter : il est donc affirmé par un test dédié plutôt que
  laissé à un gate.

  **Cinq constats n'étaient qu'une seule substitution faite par l'enregistreur.**
  `feint proxy` rédige la valeur sous toute clé JSON dont le *nom* contient
  `key` : `public_key` arrivait dans la transcription en `REDACTED`,
  `sshkey.Parse` la refusait, la création répondait 400, et la lecture puis la
  suppression qui suivaient répondaient 404 pour cette unique raison. **Le cycle
  de vie d'une clé SSH IAM était inenregistrable.** La rédaction écrit désormais
  une valeur dont le *format* prouve qu'elle est faite pour être publiée, une
  ligne de clé publique OpenSSH, lue par le même `internal/core/sshkey` avec
  lequel les packs authentifient, et rien d'autre n'a bougé. Les en-têtes gardent
  leur liste blanche, la requête garde sa liste noire (SigV4 y présigne), et un
  conteneur nommé comme un secret reste remplacé en entier. Ce dernier point
  coûte de la couverture, et le coût est dit : `ssh_keys` correspond à `key`,
  donc `ListSSHKeys` arrive dans un corpus sous forme d'une seule chaîne. La
  distinction n'est pas une préférence : un contrôle par *nom* répond « est-ce
  que ça ressemble à un secret » et jamais « est-ce que ça n'en est pas un »,
  ce qui est la raison d'être de la liste blanche des en-têtes ; une *valeur*
  qui s'identifie elle-même répond directement à la seconde question. Falsifié
  dans les deux sens, cinq mutations dans
  `tools/falsify/specs/forward-proxy.json`.

  **Deux constats venaient du rejeu, qui notait un inventaire comme une forme.**
  `fr-par-1` publie 136 types commerciaux et ce catalogue en stocke 18
  délibérément : 127 entrées d'une table dont les clés sont des *données* se
  lisaient comme 127 champs manquants, alors que `feint shapes --check` tenait
  la règle inverse sur le même artefact et n'en rapportait aucun. La règle vit
  désormais une seule fois, dans `transcript.DataKeyed`, et les deux gates la
  lisent : une clé d'une telle table est une valeur, et les valeurs ne sont
  comparées que là où un pack déclare un invariant. La reconnaissance demande
  trois enfants objets ou plus aux jeux de clés identiques, donc elle
  sous-reconnaît plutôt que l'inverse, et un champ d'une entrée que les deux
  côtés portent reste comparé.

  **Un constat était une décision écrite dans le mauvais dialecte.** Le pack
  argumentait la borne `per_volume_constraint.l_ssd` absente auprès du gate qui
  joint sur la graphie du catalogue et auprès d'aucun autre : le rejeu, qui joint
  sur le nom d'opération monté, ne rencontrait donc aucun refus et comptait neuf
  omissions délibérées comme neuf divergences. Elle est maintenant écrite dans
  les deux graphies. Les 118 types non stockés ne sont volontairement **pas**
  déclinés de la même façon : le seul chemin qui les nomme (`servers.*`) nomme
  aussi les 18 qui sont servis, et le gate d'omission publie un tel refus comme
  obsolète, ce qui fait échouer `tools/conformance/score.sh`. Mesuré, pas
  raisonné.

  `corpus/scaleway/scw-cli.jsonl` a été réenregistré le 2026-08-21 à travers
  l'enregistreur corrigé, contre le même vrai compte fr-par et selon les règles
  de `corpus/README.md` : inventaire avant, objets gratuits seulement, tout
  nommé `feint-corpus-*`, chaque destruction prouvée par une lecture à
  l'intérieur de l'enregistrement, et un inventaire de clôture identique octet
  pour octet à celui d'ouverture sur les sept familles de ressources.

### Corrigé

- **Une opération dont la description d'API dit qu'elle ne répond aucun corps
  est désormais contrôlée sur exactement cela, et trente et une opérations
  Scaleway cessent d'afficher « personne n'a regardé »** (#429). Scaleway écrit
  `204: {description: ''}` sur 64 de ses 370 opérations documentées, et c'est le
  seul fournisseur ici à le faire. C'est le fournisseur qui déclare ce que porte
  sa réponse : rien. Et ce n'est pas « les DELETE » : quatre de ses DELETE
  déclarent un corps et en répondent un, et douze des 64 ne sont pas des DELETE.

  L'extraction enregistrait cette déclaration comme l'*absence* de schéma de
  réponse, ce à quoi ressemble aussi un corps qu'elle ne sait pas nommer, et les
  deux lecteurs en aval ne voyaient donc qu'un silence. `internal/probe` sautait
  chacune de ces opérations, et le contrôle de contrat rendait la main avant
  d'enregistrer quoi que ce soit dès que le corps était vide. Un `scw instance
  server delete` répondant précisément ce que Scaleway documente était classé
  `unchecked` — la valeur que cet axe définit comme *personne n'a jamais
  regardé*.

  `noContent` porte maintenant le statut déclaré, et seulement là où le document
  déclare un 2xx sans aucun contenu. Le troisième cas reste à part et reste non
  contrôlé : `list-events`, `get-sks-cluster-inspection` et
  `list-sks-cluster-deprecated-resources` d'Exoscale déclarent un corps que cette
  extraction ne sait pas nommer, et lire leur silence comme « ne répond rien »
  serait un verdict inventé par l'axe. Ce qui est validé, ce sont les mots du
  document, dans les deux sens : un corps là où il n'en déclare aucun, et un
  statut qu'il ne nomme pas — celui-là même sur lequel un SDK généré branche pour
  décider s'il désérialise.

  Mesuré sur deux passes de `mise run conformance` ne différant que par ce
  changement, même station, même tâche : Scaleway passe de 141 à 170 de ses 173
  opérations servies sur `probed`, et de 142 à **173 sur 173** sur `contract` —
  son deuxième axe complet — tandis que toutes les autres cellules du tableau à
  sept axes, sur les trois packs, sont identiques. Les 31 opérations qui gagnent
  `contract` sont exactement les 31 opérations servies dont le document déclare
  une réponse vide, ensemble pour ensemble. Aucune ligne de code de pack n'a
  bougé. Le tableau par fournisseur est dans `docs/routes.md`.

  Trois restent à zéro sur `probed`, et la cause appartient à la sonde et non à
  un pack : `Get`, `Set` et `DeleteServerUserData` adressent une clé par son nom,
  et rien dans une passe de sonde n'en produit — un serveur que la sonde crée
  répond `{"user_data":[]}`, puisque la seule opération capable d'y poser une clé
  est celle qui en réclame une. Un client invente ce nom ; une sonde n'a pas le
  droit.

- **Outscale borne `ResultsPerPage` là où sa propre API la borne, et une taille
  de page hors de 1 à 1000 est désormais refusée** (#428). Vingt et un schémas de
  requête Read* de la description publiée par Outscale portent la même phrase —
  « between `1` and `1000`, both included » — et ce pack acceptait n'importe
  quelle valeur, lisant tout ce qui était inférieur à un comme « pas de limite ».
  `ResultsPerPage: 0`, exactement la valeur que la vraie API rejette, se voyait
  donc répondre l'inventaire entier. Un client qui envoie une taille hors bornes
  reçoit maintenant un 400 qui nomme la borne, comme en amont.
  `ReadLoadBalancers` n'est volontairement pas bornée : son schéma ne déclare
  aucun `ResultsPerPage`.

- **`ReadVmTypes` applique le filtre que le client lui envoie** (#428).
  `FiltersVmType` en déclare neuf et ce gestionnaire n'en lisait aucun : un
  client qui résolvait son type de machine par son nom recevait le catalogue
  entier avec un 200, ce qui est indiscernable d'un succès pour un client qui
  prend ensuite la première ligne. `VmTypeNames` est servi ; les huit qui
  filtrent sur l'arithmétique matérielle sont refusés par leur nom plutôt
  qu'ignorés, comme le fait déjà toute autre lecture de ce pack.

- **La `FileLocation` d'une image est déclinée plutôt qu'inventée** (#437). C'est
  l'URL de stockage objet où vivent les octets d'une OMI en amont ; cet émulateur
  ne copie aucun octet et ne sert aucun stockage objet, donc il n'existe aucune
  adresse qu'un client pourrait aller chercher. Sa voisine `BlockDeviceMappings`
  reste volontairement non déclinée : la même opération la sert quand le client
  nomme un instantané, et une déclinaison au niveau de l'opération, vraie d'un
  genre d'objet et fausse pour l'autre, est exactement la forme que #389 a coûté
  une release à comprendre.

<!-- le travail de fin de cycle, écrit à la publication depuis les pull requests fusionnées -->

### Ajouté

- **Un plan de données revendiqué exige désormais un témoin sur le runtime**
  (#486). `mise run conformance:witness` pilote les stacks d'exemple sous
  `--vm incus-ovn`, lit les revendications dans l'API de chaque pack, et lit les
  témoins **uniquement** par `incus` — demander à l'émulateur s'il a raison est
  précisément le défaut que cette porte existe pour retirer. Elle rend quatre
  verdicts : un pack qui revendique un pare-feu sans remettre de jeu de règles
  échoue nommément ; un répartiteur dont le runtime a refusé la spec échoue au
  lieu de rester enregistré et vide ; une ressource que l'API dit `running` sans
  machine derrière échoue ; et un pack qui ne revendique **rien** est sauté par
  son nom, exit 0 — sans ce dernier, la porte exigerait de Scaleway une
  propriété qu'il n'a jamais promise.

  **Une porte verte ne prouve rien, donc les quatre rouges ont été obtenus sur
  demande**, chacun en plantant le défaut qu'il nomme dans une copie hors dépôt.
  Chaque lecteur plante son contrôle positif d'abord, et trois verdicts sont
  distingués, jamais deux : « aucun témoin parce que personne n'a pu regarder »
  imprime `NOTHING WAS MEASURED` et garde exit 0. Elle vit hors de l'agrégat
  `conformance` et sur la jambe `incus-ovn` de `runtime-proof.yml`, aux mêmes
  conditions que `conformance:ssh`.

- **`/_feint/health` dit quel pack livre la répartition**, et pas seulement ce
  que le runtime sait faire (#481). `capabilities.balancing: true` était vrai —
  OVN sait réellement répartir — et ne disait rien de ce qu'un pack matérialise,
  si bien qu'un consommateur suivant la règle de ce dépôt aurait affirmé une
  propriété que Scaleway n'a pas. `enforced.balancing` publie désormais
  `["outscale"]` seul, et `TestEveryPackThatWiresTheBalancerSaysSo` tient la
  déclaration contre la source **par l'AST** : un lecteur par sous-chaîne aurait
  fabriqué un faux constat contre Exoscale, dont les commentaires nomment
  `machine.Balancer` précisément pour dire qu'il ne l'utilise pas.

- **Une nuit planifiée rouge ouvre une issue, une verte la referme** (#502).
  `runtime-proof.yml` était rouge dix nuits sur douze et la seule trace était le
  journal d'un job — le seul endroit que personne n'ouvre sans déjà savoir qu'il
  y a un problème. La logique vit dans `tools/ci/night-report.sh`, un script
  versionné plutôt qu'un bloc `run:`, parce qu'un bloc `run:` ne s'exécute que
  sur un runner GitHub et que ce dépôt porte trois cicatrices de correctifs de
  CI décrits en commentaire et jamais exécutés. Il nomme l'étape en échec et son
  mode, porte la série de verts consécutifs qu'attend #125, distingue un échec
  d'infrastructure d'un échec de mesure, et met à jour une issue au lieu d'en
  ouvrir une par nuit.

- **Un dépôt déclare le cloud contre lequel il se développe, et un seul verbe le
  lit** (#189–#192, #485). `feint.yaml` porte le provider, l'adresse de
  l'émulateur, le runtime, l'environnement et le moteur d'IaC ; `feint up` le
  lit.

### Modifié

- **Aucun Terraform ne pilote le pack Exoscale — le fork épinglé compris — tant
  que `exoscale/terraform-provider-exoscale#573` n'est pas corrigé en amont**
  (#525). Le provider publié construit deux clients d'API dont un seul honore
  `EXOSCALE_API_ENDPOINT`, donc un même `apply` ou `destroy` se scinde entre
  l'émulateur et le vrai cloud. Ce n'est pas théorique : un `feint down` lancé
  sans `TF_CLI_CONFIG_FILE` a résolu le provider publié, et cinq requêtes `GET`
  signées sont parties vers `api-ch-*.exoscale.com` avant que le run ne s'arrête
  au refresh. Rien n'a été détruit — elles portaient les identifiants factices
  volontairement publics du pack et ont été refusées à l'authentification — mais
  la seule raison pour laquelle rien de pire n'est arrivé tient à l'ordre dans
  lequel `engineEnvironment` appose les variables du pack après `os.Environ()`,
  **une propriété qu'aucun test n'affirmait**. Elle en a une désormais, et elle
  protège les trois packs.

  Le refus tombe **côté client, avant que rien ne démarre** : un refus côté
  émulateur ne servirait à rien ici, puisque ces cinq requêtes ne l'ont jamais
  atteint. Il nomme sa raison, l'issue amont, et ce qui reste possible — la CLI
  `exo` pilote ce pack de bout en bout. Le « rien n'a démarré » est mesuré, pas
  affirmé : aucun processus avant ni après, et le mtime de `feint.log` identique
  à l'octet, puisque le lancement crée ce fichier avant que l'enfant puisse
  échouer.

- **La suite Outscale pilote `octl`** (#462), la CLI qu'Outscale maintient
  désormais, sans qu'aucune opération soit perdue dans le déplacement.

- **Le bloc public émulé est un /28** (#464), ce qui évite à la suite de passer
  quatre minutes à récupérer des adresses.

### Corrigé

- **Les trois packs remettent leurs groupes de sécurité au runtime** (#475).
  Seul Scaleway le faisait : un groupe Outscale ou Exoscale était servi, renvoyé
  tel quel et réconcilié sur rien, donc tous les ports restaient ouverts quoi
  que dise le groupe. La réconciliation vit désormais une seule fois dans une
  couche neutre ; chaque pack ne garde que ce que lui seul sait — le vocabulaire
  de ses règles, qui porte quoi, et l'expansion des règles sourcées par groupe en
  /32 de leurs membres.

- **Un port qu'aucune règle n'ouvre refuse, et deux réseaux restent isolés**
  (#491). Le jeu d'isolation portait un attrape-tout `allow` à la priorité 300 là
  où le défaut de la NIC siège à 100/111 : sur tout run OVN à plusieurs
  sous-réseaux, un port interdit répondait — pour les trois packs, Scaleway
  compris. Les deux propriétés tiennent désormais ensemble, chacune avec son
  contrôle positif.

- **Les membres de pool Exoscale rejoignent les réseaux privés de leur pool**
  (#492), donc le jeu de règles du tiers applicatif a une interface à laquelle
  s'attacher ; et **un boot publie l'état que l'effet a produit** (#484) — un
  démarrage refusé publie `error` au lieu de laisser l'API appeler `running` une
  machine qui n'a jamais démarré.

- **OVN sous concurrence** (#473, #493, #519). Quinze créations de subnets
  concurrentes étaient sérialisées de 2,3 à 35,5 s, strictement linéaires, et la
  dixième était coupée à 60 s pendant que sa reprise rencontrait son propre
  subnet comme un conflit ; elles finissent maintenant ensemble à 11,6 s. Une
  destruction parallèle pouvait laisser une machine vivante, son réseau et son
  jeu de règles derrière un `Destroy complete!` ; les éditions et les
  détachements prennent leur tour, et un démontage attend ce qui tient encore
  l'instance. Quinze suppressions parallèles payaient chacune une réécriture
  d'uplink et les partagent désormais : 28,8 s deviennent 7,0 s.

  Trouvé en mesurant, et cela mérite sa ligne : **deux `ApplyFirewall`
  concurrents créaient chacun le port group OVN de l'ACL, le perdant mourait sur
  la contrainte OVSDB, et une NIC restait sans aucun jeu de règles pendant que
  l'API disait « appliqué ».** Rien d'autre n'était rouge ; c'est apparu parce
  que le brief de la branche portait les comptes `used_by` attendus comme des
  invariants.

- **Un `CreateSubnet` ordinaire ne coupe plus un peering actif** (#508). Deux
  réconciliateurs écrivaient un même état de joignabilité avec deux vérités et
  le dernier effaçait celle de l'autre ; il n'y en a plus qu'un, et le subnet
  nouveau-né rejoint le peering dans lequel il est né.

- **Le répartiteur Outscale distribue ce que l'hôte accepte, et consigne ce
  qu'il a retenu** (#483). Un arrière-plan hors du subnet faisait refuser la
  spec entière au niveau WARN, laissant un répartiteur enregistré qui ne
  transmettait rien.

- **Une exécution de conformance finit sur l'état d'hôte que son propre
  portillon accepte** (#521). Une exécution verte laissait derrière elle
  l'uplink partagé et un jeu de règles détaché, et le portillon de la suite
  refusait alors le lancement suivant. Tracé plutôt que déduit : le groupe de
  sécurité par défaut est le seul qu'aucun appel client ne peut supprimer, donc
  son ACL sur l'hôte ne pouvait tomber que par la cascade du `deleteNet` du
  pack — aucun nettoyage de suite n'aurait pu la retirer.

- **Les suites ssh nomment ce qui leur manque** (#501), au lieu de mourir en
  silence sur un `grep -c` qui rend 1 quand le compte est zéro ; et **le contrôle
  de peering mesure ce qu'il prétend** (#499), une assertion qui n'était verte
  que tant qu'aucun groupe de sécurité n'atteignait le runtime.

- **Une étape de paquets sans route sortante est dite au démarrage** (#507).
  Sous un runtime, une machine sur NIC routée n'a ni route sortante ni
  résolveur, donc `cloud-init` finissait en `status: error` dans un journal de
  machine que personne n'ouvre. Mesuré en changeant une seule variable : le même
  cloud-config sur un réseau NATé finit `done`, nginx réellement installé. La
  borne est la forme de la NIC, pas la station.

### Part avec sept limites documentées

`docs/limits.md` passe de 43 à 50 sections. Sept défauts mesurés partent avec
cette version plutôt que d'être corrigés (#518), chacun écrit avec sa mesure
datée, la séparation mesuré/déduit telle que son issue la fait, le geste qu'un
utilisateur doit en tirer, et ce qui le lèverait : la station qui n'atteint les
adresses privées OVN que par le routeur du réseau (#496), `routing_enabled` faux
par défaut là où le vrai cloud répond vrai (#497), une route publique reposée
sans idempotence au reboot (#498), un même refus documenté journalisé à deux
niveaux différents (#474), `feint images resolve` qui imprime un identifiant
incapable de démarrer (#476), la panique récupérée de la CLI `scw` sur chaque
`lb acl delete` réussi — un défaut amont, rien à corriger ici (#505), et le
second plan de la stack Exoscale portant deux ajouts d'outputs seuls (#520).

Les livrer est une décision, pas un oubli. Ce que chaque section laisse vague
est ce que son issue laisse vague, et elle le dit.

## [0.10.0] - 2026-08-20

### Ajouté

- **`feint proxy --forward` enregistre un client dont l'endpoint est codé en
  dur** (#336). Le proxy accepte `CONNECT hôte:port`, termine le TLS avec un
  certificat forgé pour la session, enregistre l'échange et le ré-émet vers
  l'hôte que le client a demandé. Rien ne change chez le client : un client Go
  qui n'installe pas de `Transport` honore `HTTPS_PROXY` tout seul, et
  `SSL_CERT_FILE` est ce qui lui fait accepter le tunnel. C'est le cas que
  `--upstream` ne pouvait pas atteindre : les collecteurs de Pépin portent leurs
  URL de base dans leur source, et rendre celles-ci configurables a été refusé
  par leur propre audit de livraison, puisque chaque requête de collecte
  transporte une clé secrète vivante. Mesuré de bout en bout le 2026-08-20
  contre un serveur HTTPS local, avec un client dont l'endpoint est une
  constante : un échange enregistré, nommé `instance/v1/API.CreateServer`, avec
  `X-Auth-Token`, `X-Consumer`, la signature en query et le `X-Session-Token` de
  la réponse tous `REDACTED`, alors que le serveur les a reçus intacts.

  **Les exigences de sécurité sont la fonctionnalité, et chacune porte sa
  falsification** (`tools/falsify/specs/forward-proxy.json`, sept mutations, qui
  mordent toutes). La redaction survit à l'interception, parce que le tunnel
  enregistre par le même `capture` : il n'existe pas de seconde porte vers le
  fichier. Boucle locale uniquement, et `--expose-to-network` est *refusé* avec
  `--forward` : un proxy détenant une autorité à laquelle un client fait
  confiance est, hors boucle locale, une machine qui déchiffre et classe tout ce
  que lui envoie quiconque peut l'atteindre. L'autorité est forgée en mémoire,
  écrite dans un unique fichier temporaire, retirée à la sortie, jamais
  installée. Et seuls les hôtes nommés sont interceptés : un `CONNECT` ailleurs
  est refusé par un 403, compté et rapporté à la fin, jamais relayé en aveugle,
  et `--forward '*'` est refusé net.

  Le transcript gagne un champ `host`, rempli par le proxy et laissé vide par
  l'anneau de l'émulateur : un enregistrement de proxy avant contient plusieurs
  clouds, et `POST /api/v1/ReadVms` n'est pas le même échange selon celui qui a
  répondu. Surface CLI en version 5. `docs/proxy.md` dit désormais ce que
  contient un enregistrement, champ par champ, et ce qu'il faut assainir avant
  de le partager — complètement, parce que l'assainissement partiel est le piège
  qui a ouvert l'audit de Pépin.

- **Un équilibreur de charge Outscale distribue de vrais paquets, à
  l'intérieur de son propre réseau** (#315). Sous `--vm incus-ovn`, la
  `PrivateIp` d'un balanceur, une adresse du Subnet où il siège, est remise à
  l'équilibreur OVN du runtime : les connexions venues de ce réseau se
  répartissent sur les Vms enregistrées, une Vm déliée cesse d'en recevoir, et
  la suppression du balanceur le retire de l'hôte. Mesuré le 2026-08-20 avec
  deux backends et un client sur un même réseau : 6/6 réponses à t0, 6/6 une
  minute plus tard, sur les deux machines à chaque fois, et 6/6 vers la
  survivante après un délien. `tools/conformance/outscale/balancer.sh` rejoue
  la mesure.

  La capacité est déclarée et vérifiée, jamais déduite d'un nom de mode :
  `/_feint/health` porte désormais `capabilities.balancing` (schéma de santé
  en version 4), seul le mode OVN la pose, la vérification au démarrage la
  retire sur un hôte sans OVN, et un binaire qui ignore la clé ne répond rien,
  ce qui vaut absence. C'est là-dessus qu'un contrôle se branche.

  **Ce qui n'a pas bougé, et pourquoi.** L'adresse publique d'un balanceur
  exposé sur internet ne route toujours nulle part : une VIP hors du réseau a
  répondu 6/6 à t0, 6/6 à t+60 s puis 0/6 à partir de t+180 s, définitivement,
  parce que le runtime n'annonce une telle adresse qu'une fois, à la création.
  Le pilote **refuse** désormais une adresse d'écoute hors du bloc du réseau
  plutôt que d'en configurer une qui s'éteindrait quelques minutes après le
  test qui l'a prouvée. `ReadVmsHealth` reste également décliné : `incus
  network load-balancer info` répond « No load-balancer health information
  available », donc rien ne sonde un backend, même ici. La famille `lb/v1` de
  Scaleway n'est pas touchée : le mécanisme est partagé, le câblage est propre
  à chaque pack, et ce pack ne demande rien au runtime. `docs/limits.md` porte
  les chiffres à côté de chaque refus.

- **Un enregistrement du vrai cloud arbitre la forme d'un Private Network**
  (#270). `feint shapes --record --provider scaleway`, lancé le 2026-08-20
  contre un vrai compte fr-par portant un Private Network, a appris 76 chemins
  de champs, dont 62 pour les objets `PrivateNetwork`, `Subnet` et `VPC`, dont
  aucun n'avait jamais été observé peuplé : l'enregistrement précédent avait été
  pris sur un compte qui n'en portait aucun, si bien que les deux formes
  d'élément étaient vides. Les 14 autres sont ceux d'un snapshot block, observés
  pour la même raison, le compte en portant un.

  Ce qu'il tranche, et que la 0.9.0 disait dans un encadré ne pas pouvoir
  trancher : la création alloue un `/64` IPv6 sans qu'on le demande, la plage
  est unique-local (`fdb2:1bb5:120a:9b::/64` sur ce compte), et deux réseaux
  d'un même projet partagent leur `/48` et ne diffèrent que par l'identifiant
  de sous-réseau, ce qui est la structure décrite par la RFC 4193. L'émulateur
  la suit maintenant, au lieu de tirer un `/48` indépendant par réseau. Le
  `Subnet` que porte une vraie lecture est exactement les huit champs déjà
  servis.

  La liste de lectures sait désormais décrire une opération qui prend un
  identifiant : une entrée finissant par `{id}` est remplie depuis la
  collection qui la précède, dans la même exécution, et le catalogue conserve
  le chemin gabarit. `GET /vpc/v2/regions/fr-par/private-networks/{id}`, la
  lecture avec laquelle le provider Terraform rafraîchit, et celle que rien
  ici ne pouvait arbitrer, est la première.

- **Les équilibreurs de charge Scaleway, cadrés par ce que les stacks
  observées appellent** (#282). `lb/v1` sert 35 opérations sur sa porte zonale :
  `ZonedAPI.CreateLB`, `ZonedAPI.GetLB`, `ZonedAPI.ListLBs`,
  `ZonedAPI.UpdateLB`, `ZonedAPI.DeleteLB`, `ZonedAPI.CreateIP`,
  `ZonedAPI.GetIP`, `ZonedAPI.ListIPs`, `ZonedAPI.UpdateIP`,
  `ZonedAPI.ReleaseIP`, `ZonedAPI.CreateBackend`, `ZonedAPI.GetBackend`,
  `ZonedAPI.ListBackends`, `ZonedAPI.UpdateBackend`, `ZonedAPI.DeleteBackend`,
  `ZonedAPI.SetBackendServers`, `ZonedAPI.UpdateHealthCheck`,
  `ZonedAPI.CreateFrontend`, `ZonedAPI.GetFrontend`, `ZonedAPI.ListFrontends`,
  `ZonedAPI.UpdateFrontend`, `ZonedAPI.DeleteFrontend`, `ZonedAPI.CreateACL`,
  `ZonedAPI.GetACL`, `ZonedAPI.ListACLs`, `ZonedAPI.UpdateACL`,
  `ZonedAPI.DeleteACL`, `ZonedAPI.CreateRoute`, `ZonedAPI.GetRoute`,
  `ZonedAPI.ListRoutes`, `ZonedAPI.UpdateRoute`, `ZonedAPI.DeleteRoute`,
  `ZonedAPI.AttachPrivateNetwork`, `ZonedAPI.DetachPrivateNetwork` et
  `ZonedAPI.ListLBPrivateNetworks` — l'attachement au réseau privé dans les
  deux écritures, parce que le SDK vendu avec la 2.43 attache sur un chemin que
  sa propre source ne lit plus. Les 19 autres sont déclinées nommément
  (statistiques sur une santé que rien ne sonde, certificats sur un TLS que
  rien ne termine, abonnés sans événement à livrer), et la porte régionale
  dépréciée l'est en bloc. Rien ne relaie de paquet et rien ne prétend le
  faire : `docs/limits.md` dit ce que vaut un 200 ici.

- **Les passerelles publiques Scaleway** (#282). `vpcgw/v2` sert 15 opérations :
  `API.CreateGateway`, `API.GetGateway`, `API.ListGateways`,
  `API.UpdateGateway`, `API.DeleteGateway`, `API.CreateGatewayNetwork`,
  `API.GetGatewayNetwork`, `API.ListGatewayNetworks`,
  `API.UpdateGatewayNetwork`, `API.DeleteGatewayNetwork`, `API.CreateIP`,
  `API.GetIP`, `API.ListIPs`, `API.UpdateIP` et `API.DeleteIP`. `vpcgw/v1` est
  déclinée en bloc, et pas parce que la v2 la remplace : le portail ne publie
  plus de document v1, et chaque route montée est vérifiée contre ce document.
  Un provider épinglé sous 2.52 rencontre un 501 nommé plutôt qu'un silence.

- **Les groupes de placement Scaleway, sur les deux portes** (#285). Un refus
  retiré : la famille était déclinée avec « toute politique serait rapportée
  satisfaite quoi qu'elle demande », et mesurer ce que le provider fait de la
  réponse a transformé cette phrase en obligation plutôt qu'en refus — 2.43.0
  et 2.81.0 stockent toutes deux `policy_respected` en attribut calculé sur
  lequel elles ne conditionnent rien. `instance/v1` sert désormais
  `API.CreatePlacementGroup`, `API.GetPlacementGroup`, `API.ListPlacementGroups`,
  `API.UpdatePlacementGroup`, `API.SetPlacementGroup`,
  `API.DeletePlacementGroup`, `API.GetPlacementGroupServers`,
  `API.SetPlacementGroupServers` et `API.UpdatePlacementGroupServers` ;
  `instance/v2alpha1` sert les cinq sur lesquelles le provider 2.81.0 a déplacé
  le CRUD de la ressource (`API.CreatePlacementGroup`, `API.GetPlacementGroup`,
  `API.ListPlacementGroups`, `API.UpdatePlacementGroup`,
  `API.DeletePlacementGroup`). Le placement est enregistré, jamais appliqué, et
  `policy_respected` dit la vérité de l'hôte unique plutôt que la flatteuse.

- **Les équilibreurs de charge Outscale** (#281). Un refus retiré, cadré sur ce
  que trois stacks observées appellent réellement, mesuré avec
  `feint proxy --record` plutôt que lu sur les 23 opérations du SDK :
  `osc/Client.CreateLoadBalancer`, `osc/Client.UpdateLoadBalancer`,
  `osc/Client.DeleteLoadBalancer`, `osc/Client.RegisterVmsInLoadBalancer`,
  `osc/Client.LinkLoadBalancerBackendMachines` et
  `osc/Client.UnlinkLoadBalancerBackendMachines` — les deux écritures de
  l'attachement, parce que la mesure a renversé la lecture de la source 1.1.3.
  Le reste de la famille reste décliné nommément ; la première stack qui en
  appelle une la rouvre.

- **Une version doit désormais dire ce qu'elle se met à servir et ce qu'elle
  cesse de servir** (#326). `mise run release:surface` compare les
  `coverage/*-coverage.json` versionnés du dernier tag à ceux de cet arbre et
  refuse (code 2) qu'une opération qui a changé de camp ne soit nommée ni dans
  `CHANGELOG.md` — qui *est* le corps de la release — ni dans
  `tools/release/unnamed.json`, où « pas la peine de le nommer » se signe avec
  une raison. Trois transitions doivent être nommées : nouvellement servie,
  retirée, et **un refus retiré**, celle qui coûte en silence. La 0.9.0 a monté
  les interfaces de réseau privé `instance/v2alpha1` sans le dire nulle part ;
  un consommateur aval y a passé une journée à sonder deux binaires côte à côte
  pour découvrir qu'un 501 était devenu un 200, et contournait par ailleurs
  trois refus devenus des fonctionnalités depuis des semaines. Lancé sur ce
  train, le contrôle a nommé 70 opérations qu'aucune note ne portait : les
  quatre entrées ci-dessus.

- **Une version déclare les versions de clients qui l'ont prouvée** (#325).
  `docs/clients.md` est généré depuis les épinglages du workflow de conformance
  et depuis chaque bloc `required_providers` sous `tools/conformance/` et
  `examples/stacks/`, publié dans le corps de la release, et vérifié par
  `feint docs --check` comme toute autre page générée. Une contrainte qui
  n'existe nulle part s'affiche *non épinglé* au lieu d'être inventée : deux
  stacks résolvent leur provider à neuf à chaque exécution, et l'artefact le
  dit. Le consommateur d'où vient cette demande résout ses providers à neuf en
  CI, si bien qu'une version Scaleway lui parvient le lendemain de sa
  publication, que cet émulateur l'ait rattrapée ou non.

- **Une mesure sait désormais qui lui a répondu** (#309). `GET /_feint/health`
  gagne `instance` (le pid et l'heure de démarrage du processus qui répond) et
  son `schema_version` passe à 3 (additif : chaque champ de la version 2 est
  inchangé). Le champ existe parce que son absence a été mesurée : le
  2026-08-19, un émulateur résiduel sur un port partagé a répondu à une sonde
  avec le catalogue de la version précédente, et rien dans la réponse ne
  pouvait le dire.

- **`brew install stephrobert/feint/feint`, avec des empreintes dérivées de la
  release et non recopiées** (#321). La release publiait déjà des binaires
  macOS signés, et un lecteur macOS devait quand même trouver la page de
  release, choisir une architecture et vérifier une somme à la main. La
  décision que portait l'issue était *qui écrit la formule à chaque release*,
  et la réponse n'est ni l'une ni l'autre des deux moitiés évidentes :
  `mise run release:formula` récupère le `checksums.txt` signé par cosign de la
  release et en **dérive** toute la formule, donc remplir le tap coûte une
  copie et jamais une transcription ; `mise run release:tap` la dérive à
  nouveau et sort en 2 tant que le tap sert autre chose, chaque jour
  (`.github/workflows/tap.yml`). Une poussée depuis `release.yml` a été refusée
  pour la raison que ce dépôt a déjà écrite deux fois : elle demanderait un
  jeton inter-dépôt qui n'existe pas, et *un gate qui répare le dépôt est une
  seconde porte d'entrée*. La formule installe les octets publiés et ne
  recompile jamais : ce que Homebrew vérifie est donc ce que la release a
  signé. Les refus sont dans `internal/release/formula.go`, falsifiés par
  `tools/falsify/specs/homebrew-formula.json` : la liste de sommes est
  récupérée par le réseau, donc une entrée pour laquelle la formule n'a pas de
  plateforme l'arrête au lieu d'être ignorée, une empreinte qui n'est pas un
  SHA-256 n'atteint jamais le fichier, et aucun nom venu de cette liste ne
  devient une URL ou un littéral Ruby sans contrôle. Prouvé avec le vrai client
  et non par un rendu : le 2026-08-20, contre Homebrew 5.1.15, la formule
  dérivée placée dans un tap a installé le binaire v0.9.0 publié,
  `feint version` a répondu `v0.9.0`, `brew test` est passé, `brew audit` n'a
  rien signalé, et un octet retourné dans une empreinte a fait échouer la même
  installation sur *Formula reports different checksum*. **Le tap n'existe pas
  encore** : `mise run release:tap` sort en 2 et nomme la commande qui le
  remplit.

- **Le contrat qu'on demande à la stack d'un tiers** (#327). Un consommateur
  aval a proposé la lane qui a trouvé la rupture Scaleway 2.81.0 comme seizième
  stack observée, et a demandé quel contrat nous voulions qu'elle respecte. Il
  est écrit dans `examples/stacks/README.md`, avec la décision qu'il a forcée :
  une telle stack est **recensée et rejouée à la demande, jamais câblée dans la
  CI de ce dépôt**. Le dépôt d'un tiers change sans notre décision, donc un gate
  obligatoire posé dessus peut virer au rouge pour une raison que personne ici
  n'a choisie — et un rouge que personne ne peut traiter est ce qui apprend aux
  gens à sauter un gate. `examples/stacks/surveyed.md` consigne l'offre avec
  ses chiffres attribués comme les leurs, et chaque case que nous ne pouvons
  pas remplir nommée comme non mesurée.

### Corrigé

- **Attacher une NIC privée ne met plus toutes les machines en file derrière la
  plus lente** (#348). Six attachements sur la stack d'exemple Scaleway en
  `--vm incus-ovn` ont pris 26 s, 31 s, 42 s, 52 s, 63 s et 73 s — dix secondes
  de plus à chaque fois, c'est-à-dire le travail d'une machine repayé par toutes
  celles qui la suivent. Passé la minute, le client abandonne et rejoue, et son
  rejeu tombe sur l'interface que sa propre première tentative avait créée :
  *le serveur est déjà attaché à ce réseau privé*. Le pilote tenait un unique
  mutex de paquet pendant tous ses appels dans la machine ; il prend désormais
  le verrou par cible du dépôt, indexé par machine — la portée qu'a toujours eue
  la collision de noms qu'il protège. Mesuré sur `main` sans cette branche : la
  même pente, donc la file préexiste et n'était qu'inatteignable derrière
  #341.
- **Un passage complet `incus-ovn` ne fait plus supprimer deux fois la même
  chaîne de pare-feu au démon** (#341). L'échec — `Failed deleting nftables
  chain "fwd.feint-uplink": No such file or directory`, qui tuait la suite
  outscale-tofu — se reproduit **depuis une station vierge** : la lecture « état
  accumulé entre les passages » n'était vraie qu'à moitié. Mesuré avec `incus
  monitor` sur un passage entier : `feint clean` en fin de suite oapi-cli
  supprime l'uplink, puis le parallélisme par défaut d'OpenTofu recrée d'un coup
  deux subnets et un réseau machine par défaut. Un `PUT` sur l'uplink et un
  `POST` de réseau OVN qui s'y rattache font tous deux reconstruire le pare-feu
  nftables de l'uplink, et le `removeChains` d'Incus liste puis supprime sans
  verrou partagé entre ces deux chemins — la chaîne du perdant est donc
  supprimée par l'opération concurrente *entre son propre relevé et sa
  suppression*. `uplinkMu` sérialise désormais toute opération qui fait
  reconstruire l'uplink, le `network create` compris.
- **Un réseau OVN supprimé retire son bloc délégué de l'uplink** (#341).
  `RemoveNetwork` ne le faisait jamais, si bien qu'un seul passage en accumulait
  neuf — les sept routes signalées par l'issue n'étaient pas le résidu de sept
  exécutions. Un uplink laissé par une exécution morte est aussi adopté une fois
  par processus, en retirant les routes des réseaux disparus ; et un uplink tenu
  par un émulateur **vivant** est refusé plutôt que partagé, le partage entre
  processus étant la même corruption sans verrou sous un autre nom.
- **`feint doctor` demande si un service DHCP survit à son réseau, et non à son
  interface** (#342). Il répondait `ok` pendant qu'un orphelin tenait
  `10.50.2.1` et faisait échouer la conformance suivante, parce qu'il cherchait
  un service dont l'interface avait disparu — or l'interface avait survécu *avec*
  son service. Les deux avaient survécu au réseau, et c'est la question que
  personne ne posait. Un reste est désormais une ligne rouge qui nomme le bloc
  et le pid, `feint clean` tue le service et **dit ce qu'il ne touchera pas** —
  un pont sans étiquette n'est pas démontrablement à nous — et chaque ligne
  verte de `doctor` a été relue contre ce qu'elle mesure réellement.
- **Une suite de conformité ssh refuse de démarrer quand l'émulateur ne détient
  aucune des images qu'elle démarre, et la preuve runtime les construit**
  (#335). `runtime-proof.yml` échouait sur son étape *Scaleway ssh suite* cinq
  nuits planifiées d'affilée, du 2026-08-16 au 2026-08-20, sur les deux jambes.
  Le correctif était imprimé dans son propre journal chacune de ces nuits :
  `WARN no image of ours for this system, booting the upstream one … fix="feint
  images"`, et rien ne l'exécutait. La sous-commande n'apparaît dans aucune
  étape de ce workflow ni dans aucune ligne de `mise run conformance:ssh`.

  Reproduit avant d'être corrigé, sur une station qui détenait les images, en
  supprimant les cinq alias `feint/*` puis en les remettant : la même suite est
  passée en 21 secondes avec eux et a échoué en 93 sans, sur la phrase même que
  la CI imprimait. Le workflow lance désormais `feint images` avant de démarrer
  l'émulateur, et `tools/conformance/guard.sh` a gagné `guard_images`, que les
  trois suites ssh appellent avant d'enregistrer une clé : il lit `.machines`
  sur `/_feint/health`, interroge `feint images --check` sur ce runtime, et
  refuse en un vingtième de seconde en nommant la commande, au lieu d'échouer
  quatre-vingt-dix secondes plus tard sur « no ssh daemon answered », qui
  accuse l'adresse. La garde vit dans le fichier partagé, pas dans chaque
  suite, pour la raison que donne CLAUDE.md : un contrôle recopié trois fois
  est un contrôle que la quatrième oublie. Falsifié par
  `tools/falsify/specs/ssh-suite-needs-its-images.json`, cinq mutations, dont
  celle où une suite garde la garde et cesse de l'appeler.

  **Pourquoi le repli avait cessé d'en être un**, et c'est la partie qui mérite
  d'être retenue. #203 a choisi de démarrer l'image amont quand l'émulateur ne
  détient aucune des siennes, délibérément, pour qu'un premier contact
  fonctionne. #202 a ensuite donné à une machine exactement la seule adresse que
  publie son fournisseur, sur une NIC routée sans NAT. Les deux vont bien
  séparément et pas ensemble : mesuré le 2026-08-20, le cloud-init d'une image
  amont meurt sur `Temporary failure resolving 'archive.ubuntu.com'`,
  `openssh-server` ne s'installe jamais, et rien n'écoute sur le port 22.
  L'avertissement de l'émulateur disait « la machine installe un démon ssh au
  premier démarrage et a besoin du réseau sortant pour cela », ce qui était vrai
  à l'écriture et était devenu la description d'un événement impossible. Il dit
  maintenant ce qui se produit vraiment.

  Cela débloque aussi le compteur de #125 : son critère de promotion est une
  série de nuits planifiées vertes consécutives, donc tant que ceci restait
  cassé ce nombre était bloqué à zéro par construction, et rien ne le disait.

- **La surface CLI gelée est lue depuis les jeux de drapeaux que le binaire
  enregistre, plus depuis l'aide qu'il imprime** (#334). `feint proxy
  --intercept` est parti en v0.9.0 : le binaire l'acceptait, `feint proxy
  --help` l'affichait, et `internal/cli/testdata/frozen/cli.json` ne le listait
  pas, six jours durant. Cette fixture est la surface que #132 a gelée pour
  qu'un pipeline extérieur à ce dépôt puisse s'y fier. La cause tenait à
  l'observation elle-même : elle parsait l'aide rendue par `feint --help`, donc
  elle consignait ce que l'aide *prétendait*. Le drapeau manquant n'était que la
  moitié la moins chère. Un drapeau **retiré** d'un jeu alors que sa ligne
  d'aide survit aurait laissé le même gate au vert, et c'est la direction qui
  casse un consommateur.

  La surface vient désormais de `flag.FlagSet.VisitAll`, par une couture unique
  où chaque verbe construit ses drapeaux (`internal/cli/flagset.go`). L'aide
  garde une promesse, mais sous la forme d'un contrôle qui a son propre sujet :
  `TestTheHelpNamesEveryFlagTheBinaryAccepts` compare les deux listes dans les
  deux sens, de sorte qu'un drapeau accepté par le binaire et nommé par aucun
  bloc d'aide échoue, et qu'un bloc d'aide nommant un drapeau qu'aucun jeu
  n'enregistre échoue aussi. Falsifié dans les deux sens par
  `tools/falsify/specs/frozen-cli-surface.json`, sept mutations, chacune rejouée
  contre le test qui doit mordre.

- **La surface CLI passe en version 5, et 24 drapeaux que le binaire a toujours
  acceptés y sont visibles pour la première fois** (#334). C'est l'observation
  qui a bougé, pas le binaire : `--intercept` et `--expose-to-network` sur
  `proxy`, `--shapes` et `--expose-to-network` sur `serve`, `--check` sur
  `version`, les six drapeaux de `serve` que `start` accepte réellement, trois
  sur `evidence` et dix sur `docs`. Trois entrées sont parties, et les trois
  appartenaient au parseur : `--version` et `-v` sous `version`, qui sont des
  alias du verbe et non ses drapeaux (un lecteur qui tapait `feint version
  --version` s'entendait répondre `flag provided but not defined`), et
  `--state` sous `snapshot`, qui venait d'une phrase parlant de `serve`.
  `snapshot` est désormais indexé par jeu de drapeaux (`snapshot save`,
  `snapshot load`, `snapshot list`), ce qui est la façon de dire que `--force`
  appartient à `save` seul.

  `feint --help` a gagné tous les drapeaux qu'il cachait, dont les deux
  `--expose-to-network`, ceux qu'un lecteur a le plus besoin de rencontrer avant
  de les poser.

- **`docs/proxy.md` a cessé de refuser un outil que ce dépôt livre** (#334). La
  page annonçait au lecteur que l'interception « est #76 et délibérément pas cet
  outil », pendant que `docs/limits.md` envoyait ce même lecteur s'en servir.
  `feint proxy --intercept` existe depuis la v0.9.0 ; la page le documente
  désormais : ce qu'il forge, ce qu'il imprime, et la seule chose qu'il ne fera
  pas, à savoir installer quoi que ce soit dans le magasin de confiance du
  système ou modifier le `/etc/hosts` de l'opérateur. #76 et #92 sont closes,
  livrées.

- **Une adresse attachée après le lancement atteint une machine qui ne joint
  aucun réseau, et un groupe de sécurité y devient une limite déclarée plutôt
  qu'un silence** (#337). Depuis #202, une machine qui ne porte qu'une adresse
  publique la porte sur une NIC `routed`, qui n'a pas de clé `network` ; or les
  deux chemins d'adresse de `internal/core/machine` sélectionnaient les
  interfaces par cette clé. Toute adresse routée après le lancement mourait sur
  « machine has no network interface » : l'IP élastique de la suite ssh
  Exoscale était annoncée attachée par l'API sans que rien ne la pose sur la
  machine, et chaque replay de poweron Scaleway journalisait la même erreur
  au-dessus d'une adresse qui fonctionnait. La NIC routée est désormais
  reconnue, et le mécanisme est le sien : l'adresse rejoint l'`ipv4.routes` du
  device (mesuré accepté sur Incus 7.2, à froid comme à chaud), et le
  rebranchement qu'une édition à chaud provoque (mesuré : l'interface invitée
  revient éteinte et nue) est réparé depuis la configuration du device
  lui-même. La suite ssh Exoscale passe de bout en bout.

  La moitié pare-feu ne pouvait pas se corriger de la même façon, parce que la
  mesure dit non : une NIC routée n'accepte aucune option de sécurité, chaque
  clé étant une « Invalid device option » sur Incus 7.2 comme 7.3 (table dans
  `docs/limits.md`). Y appliquer un jeu de règles était une ligne ERROR que le
  plan de contrôle recouvrait en répondant comme si le groupe était appliqué.
  Le refus est désormais déclaré plutôt que maquillé : `/_feint/health` gagne
  `capabilities.firewall_public_only` (schéma de santé en version 5), faux
  dans tous les modes Incus ; `ApplyFirewall` répond l'erreur typée
  `machine.ErrFirewallUnenforceable` au lieu d'émettre des clés condamnées,
  tout en couvrant encore les interfaces qui savent appliquer ; et un groupe
  qui ne filtre rien (celui par défaut, embarqué par chaque `scw instance
  server create`) n'attache rien sur aucun runtime, seule traduction fidèle de
  « ne filtre rien ». Falsifié par `tools/falsify/specs/routed-nic.json`.

- **Une stack appliquée à chaque pull request épingle le provider qui a
  répondu, et une stack que la CI n'applique pas dit pourquoi** (la table de
  #325, premier jour). La page générée des clients a révélé deux choses que
  rien n'avait dites : `examples/stacks/outscale/modules/net` était appliqué à
  chaque pull request sans déclarer la moindre contrainte de provider —
  `terraform init -upgrade` le résolvait depuis tout le registre à chaque
  exécution, donc l'apply prouvait que l'émulateur avait répondu à ce qui était
  le plus récent ce matin-là, et rien de rejouable — et
  `examples/stacks/exoscale` n'est appliqué par rien, ce qui est une bonne
  décision écrite en prose seulement, dans trois fichiers, en trois
  formulations, vérifiée par rien. Le module porte désormais le même plancher
  `~> 1.7` que sa racine, et `feint docs --check` sort en 2 sur une stack
  appliquée sans contrainte, sur une stack non appliquée que rien ne déclare,
  et sur une déclaration visant une stack disparue ou que la CI s'est mise à
  appliquer. La raison est imprimée sur `docs/clients.md` depuis la liste même
  que lit le refus. Falsifié par `tools/falsify/specs/stack-proof.json`.

- **Un Private Network et un VPC servent le drapeau Object Storage que le vrai
  cloud porte** (#270). `has_s3_integration` et `s3_integration_enabled`
  étaient déclarés par le contrat, renvoyés par chaque réponse réelle, et
  absents ici. Les deux sont invisibles à travers `scw`, qui les laisse tomber
  en chemin vers sa propre sortie : seul un enregistrement pouvait les
  trouver, et seulement un enregistrement pris pendant que les objets
  existaient.

- **Les deux créations `vpc/v2` répondent 200, ce que portait le fil** (#270).
  `CreateVPC` et `CreatePrivateNetwork` répondaient 201, le statut qu'écrit
  toute autre création du pack ; les deux ont été mesurées à 200 sur un vrai
  compte, lues dans une transcription `feint proxy` plutôt que sur un CLI qui
  n'en montre aucun. Aucun autre produit n'a été mesuré, donc aucun autre n'a
  bougé. Cela ne change rien pour un client qui teste 2xx, et cela change ce
  que cet émulateur a le droit d'affirmer.

- **Un identifiant n'atteint jamais un catalogue de formes versionné** (#270).
  L'enregistrement d'une ressource porte le chemin de cette ressource, et ce
  chemin partait verbatim dans la clé d'opération et dans `Operation.Path` :
  la première entrée de liste de lectures visant un objet unique aurait donc
  commité l'UUID du compte de quelqu'un dans `shapes/*.json`. Les chemins sont
  désormais anonymisés à la frontière où un enregistrement devient un
  artefact, quel qu'en soit l'auteur, et un test lit les fichiers versionnés
  eux-mêmes au lieu de faire confiance à la règle.

- **`feint shapes --check` nomme ce qu'il n'a pas pu comparer** (#270). Onze
  opérations enregistrées sortaient de son arithmétique sans un mot :
  l'émulateur répond un refus hors ligne et la comparaison était sautée en
  silence. La ligne de couverture les liste maintenant comme non contrôlées,
  ce qui est la différence entre « rien ne va mal » et « rien n'a été
  regardé ».

- **`server.public_ips` répond dans l'ordre que la création a nommé** (#320).
  `Server.public_ips` chez Scaleway est une liste et Terraform la stocke comme
  telle : le provider reconstruit `ip_ids` index par index depuis elle, et son
  chemin d'application est une suite d'appels `UpdateIP` par ensembles,
  incapable de réordonner. Un émulateur répondant les adresses attachées dans
  l'ordre du store faisait donc replanifier sans fin la même permutation à
  tout `ip_ids = [a, b]` dont l'ordre du store était `[b, a]` ; mesuré sur
  `sergelogvinov/terraform-talos` au moment où ses serveurs se sont appliqués
  pour la première fois. Chaque attachement enregistre désormais la position
  que le client lui a donnée : à la création (`public_ips`/`public_ip`), sur
  `UpdateServer.public_ips` (déclaré par le SDK et lu par personne ici : un
  PATCH le nommant répondait 200 sans rien changer), et sur un attachement nu
  par `PATCH /ips`, qui rejoint la fin de la liste. Les identifiants
  eux-mêmes étaient déjà les UUID nus de l'API ; la moitié `id → fr-par-1/id`
  du diff observé est le provider qui normalise son propre état, et elle part
  avec la permutation.

- **`feint start` refuse une réponse venant d'un processus qu'il n'a pas
  lancé** (#309). Avant, quand quelque chose tenait déjà l'adresse, l'enfant
  lancé mourait sur l'erreur de bind pendant que `start` prenait la réponse de
  santé de l'occupant pour celle de l'enfant : il affichait « listening
  (pid N) » à propos d'un pid déjà mort, `feint wait` disait prêt, et chaque
  suite mesurait alors ce que servait l'occupant ; reproduit contre une
  version résiduelle avant le correctif. `start` compare désormais
  `instance.pid` avec l'enfant qu'il a lancé et sort en 1 en nommant
  l'occupant (pid, heure de démarrage) ; `feint serve` refuse d'emblée une
  adresse où un émulateur répond déjà, au lieu de laisser le fait dans une
  erreur de bind qu'un wrapper avale. Une seconde exécution après un arrêt
  propre n'est pas touchée : le garde compare des identités, il ne compte pas
  les exécutions.
- **`FEINT_ADDR` est de nouveau honoré** (#309). Il était déclaré en littéral
  dans le `[env]` de `mise.toml`, qui l'emporte sur une variable exportée :
  `FEINT_ADDR=127.0.0.1:4699 mise run conformance` utilisait silencieusement
  4599, donc toutes les exécutions parallèles convergeaient sur le port qu'un
  émulateur résiduel avait le plus de chances de tenir. La déclaration lit
  désormais l'environnement d'abord et ne garde 4599 que comme défaut.

## [0.9.0]

La version du contrat. Feint se consomme désormais directement depuis un test Go
ou un job de CI — `feinttest.Start(t)`, `stephrobert/setup-feint@v1`, l'image OCI
en service — et chacune des 285 opérations montées est soit pilotée par un vrai
client, soit accompagnée, à la route, de la raison pour laquelle aucun client
officiel ne l'atteint. Le modèle de compatibilité a cessé d'être une promesse :
surfaces gelées, contrôle de compatibilité joué contre les consommateurs avant le
tag, vérification des réponses qui cherche aussi ce qui *manque*, et quinze stacks
Terraform tierces appliquées contre l'émulateur. Exoscale gagne le stockage bloc
et les instance pools.

> **Cette section a été complétée le 2026-08-19, après le tag** (#326, #325).
> Rien de ce qui était déjà écrit plus bas n'a été modifié, et ni le tag, ni les
> binaires, ni l'image n'ont bougé : seul ce que cette version dit d'elle-même a
> changé. Un consommateur aval qui fait tourner la 0.9.0 comme gate de CI sans
> aucun credential a dû sonder les binaires v0.8.0 et v0.9.0 côte à côte pour
> apprendre que cette version sert `instance/v2alpha1` : la chaîne n'apparaît
> nulle part dans la section d'origine, et `2.81.0` non plus, la version du
> provider sur laquelle la suite Scaleway a été épinglée précisément pour
> l'exercer. Les trois blocs qui suivent sont cette moitié manquante. Ils sont
> dérivés de `coverage/*-coverage.json` aux deux tags plutôt que rédigés de
> mémoire, ce qui est la seule raison pour laquelle on peut s'y fier à cette
> distance.

### Ce que cette version sert et que la 0.8.0 ne servait pas *(consigné le 2026-08-19)*

Dérivé de `coverage/<provider>-coverage.json` tel que versionné à `v0.8.0` et à
`v0.9.0` : une opération compte comme nouvellement servie quand son statut passe
à `implemented`, depuis `declined` ou depuis la colonne non triée. Les totaux
concordent avec le bloc de couverture généré dans le README de chaque tag (220
routes montées, puis 285).

| Provider | Servies en 0.8.0 | Servies en 0.9.0 | Nouvellement servies | Non triées en 0.8.0 |
|---|---|---|---|---|
| Scaleway | 102 / 315 | 107 / 315 | 5 | 0 |
| Outscale | 72 / 263 | 85 / 263 | 13 | 18 |
| Exoscale | 46 / 374 | 93 / 374 | 47 | 75 |
| **Total** | **220** | **285** | **65** | **93** |

Une partie de ces opérations est décrite en prose plus bas : #12 pour le stockage
bloc Exoscale, #232 pour les instance pools, #161 pour les réseaux privés, #172
pour la moitié en place d'Outscale et les DHCP options. **Aucune n'était nommée
en tant qu'opération**, et le nom d'opération dans le dialecte du provider est
exactement la chaîne qu'un consommateur cherche au grep le jour où son provider
change sous lui. D'où les listes.

- **Scaleway, 5 opérations, et le seul changement de statut de cette surface
  entre les deux tags : les interfaces de réseau privé `instance/v2alpha1`**
  (#257, #260). `CreatePrivateNetworkInterface`,
  `DeletePrivateNetworkInterface`, `GetPrivateNetworkInterface`,
  `ListPrivateNetworkInterfaces`, `UpdatePrivateNetworkInterface`, déclinées en
  0.8.0, servies ici.

  Ce que cela vaut pour un consommateur : `scaleway/scaleway` 2.81.0, publié le
  2026-08-17, *crée* toujours une NIC privée par `instance/v1` mais la lit, la
  crée et la supprime par `instance/v2alpha1/private-network-interfaces`, où
  l'interface est une ressource de premier niveau portant `server_id` au lieu
  d'être une sous-ressource du serveur. Contre la 0.8.0, cet apply se termine
  sur un 501. Une lane épinglée à 2.80.0 ou en dessous pour rester verte contre
  la 0.8.0 peut passer à 2.81.0 contre cette version, et
  `tools/conformance/scaleway/terraform/main.tf` épingle exactement 2.81.0,
  pour que la suite exerce ces cinq opérations plutôt qu'une version qui ne les
  appelle jamais.

- **Outscale, 13 opérations**, toutes les treize sorties de la colonne non
  triée (#172, #177, #198) : `AcceptNetPeering`, `CreateDhcpOptions`,
  `CreateNetPeering`, `DeleteDhcpOptions`, `DeleteNetPeering`,
  `LinkPrivateIps`, `ReadNetPeerings`, `RejectNetPeering`, `UnlinkPrivateIps`,
  `UpdateNet`, `UpdateNic`, `UpdateRouteTableLink`, `UpdateSubnet`. Les cinq
  restantes ont quitté cette colonne par l'autre porte, déclinées nommément :
  `CheckAuthentication`, et les quatre opérations `NetAccessPoint`.

- **Exoscale, 47 opérations**, toutes les quarante-sept sorties de la colonne
  non triée (#12, #161, #173, #232) : la famille du stockage bloc
  (`create-block-storage-volume`, `get-block-storage-volume`,
  `list-block-storage-volumes`, `update-block-storage-volume`,
  `resize-block-storage-volume`, `delete-block-storage-volume`,
  `attach-block-storage-volume-to-instance`, `detach-block-storage-volume`,
  `create-block-storage-snapshot`, `get-block-storage-snapshot`,
  `list-block-storage-snapshots`, `update-block-storage-snapshot`,
  `delete-block-storage-snapshot`), les instance pools
  (`create-instance-pool`, `get-instance-pool`, `list-instance-pools`,
  `update-instance-pool`, `scale-instance-pool`, `evict-instance-pool-members`,
  `reset-instance-pool-field`, `delete-instance-pool`), les réseaux privés
  (`create-private-network`, `get-private-network`, `list-private-networks`,
  `update-private-network`, `reset-private-network-field`,
  `delete-private-network`, `attach-instance-to-private-network`,
  `detach-instance-from-private-network`,
  `update-private-network-instance-ip`), les snapshots et les templates
  (`create-snapshot`, `get-snapshot`, `list-snapshots`, `delete-snapshot`,
  `revert-instance-to-snapshot`, `promote-snapshot-to-template`,
  `copy-template`, `register-template`, `update-template`, `delete-template`),
  et `add-external-source-to-security-group`,
  `remove-external-source-from-security-group`, `enable-tpm`, `list-events`,
  `get-organization`, `reset-elastic-ip-field`, `reset-instance-field`.

  Vingt-huit autres ont quitté la colonne non triée déclinées nommément (#173,
  #300) : les onze opérations NLB, les seize opérations VPC marquées `[BETA]`,
  et `export-snapshot`.

**Rien de ce que cette version a cessé de servir.** Aucune opération n'est
passée de `implemented` à `declined` entre les deux tags, et aucune opération
n'a quitté la surface amont d'aucun provider. C'est la direction la plus
dangereuse, et elle est énoncée ici plutôt que laissée à déduire d'un silence.

### Trois refus qu'un consommateur tenait pour acquis : deux avaient déjà disparu *(consigné le 2026-08-19)*

Le même rapport aval listait trois refus qu'il approuvait et dont il ne demandait
pas la levée. Mesuré contre les artefacts, **deux des trois avaient été levés
avant la sortie de la 0.9.0, et le troisième tient toujours dans cette version**.
Un refus retiré porte autant qu'une route ajoutée : le consommateur continue de
construire autour d'une absence qui n'existe plus, et rien nulle part ne devient
rouge. Ce projet ne publiait aucune des deux directions.

- **Un volume racine Scaleway de type `sbs_volume` est honoré**, depuis #8, qui
  est sorti en **0.8.0** et non ici. `tools/conformance/scaleway/terraform/main.tf`
  déclare le bloc, et l'apply, le second plan vide et le destroy passent tous.
  Ce qui reste surchargé, ce sont les types *locaux* (`l_ssd`, `scratch`), pour
  la raison que `docs/limits.md` donne à ce tag : le catalogue émulé déclare
  `volumes_constraint.min_size` à 0 et le CLI somme les volumes locaux contre
  cette valeur, donc en attacher un ferait refuser au CLI la création qu'il
  vient lui-même de demander. `b_ssd` ne planifie pas non plus, et c'est la
  décision du provider depuis la 2.79, pas celle de cet émulateur.

- **`ipam/v1/API.BookIP` est servie**, et avec elle `ReleaseIP`, `ReleaseIPSet`,
  `UpdateIP`, `AttachIP`, `DetachIP` et `MoveIP`.
  `coverage/scaleway-coverage.json` porte `BookIP` en `declined` à `v0.7.0` et
  en `implemented` à partir de `v0.8.0` : elle a été levée par SW-4, la première
  moitié de #11, et la 0.9.0 en a hérité. Un plan portant un `scaleway_ipam_ip`
  fonctionne, et une adresse réservée sort du subnet du Private Network
  lui-même au lieu d'être inventée.

- **`osc/Client.CreateLoadBalancer` est toujours déclinée en 0.9.0**, et sur ce
  point le rapport avait raison. `internal/providers/outscale/declined.go` la
  porte à ce tag avec le reste de la famille LBU, sur la raison énoncée qu'*un
  load balancer est un plan de données qui accepte de vraies connexions, et que
  l'émulateur n'en a aucun*. `ReadLoadBalancers` est l'exception délibérée et
  répond une liste vide, parce que décliner une lecture dont la réponse honnête
  est « aucun » a cassé un `terraform destroy` mesuré. La famille est servie sur
  `main` depuis #281, qui a atterri après ce tag ; la 0.9.0 ne la sert pas.

### Les versions de clients contre lesquelles la 0.9.0 a été prouvée *(consigné le 2026-08-19)*

Toute affirmation de cette note est vraie de ces clients et d'aucun autre. Ce
sont les versions que le workflow de conformance installe et lance, lues au tag
plutôt que de mémoire ; les chemins et numéros de ligne sont ceux de `v0.9.0`.

| Client | Version | Où le nombre est écrit, à `v0.9.0` |
|---|---|---|
| `scw` | 2.56.3 | `.github/workflows/conformance.yml:27` (`SCW_VERSION`) |
| Terraform | 1.13.3 | `.github/workflows/conformance.yml:28` (`TERRAFORM_VERSION`) |
| OpenTofu | 1.12.5 | `.github/workflows/conformance.yml:356` (`TOFU_VERSION`) |
| `oapi-cli` | 0.15.0 | `.github/workflows/conformance.yml:273` (`OAPI_VERSION`) |
| `exo` | 1.95.6 | `.github/workflows/conformance.yml:310` (`EXO_VERSION`) |
| provider `scaleway/scaleway` | **2.81.0, exact** | `tools/conformance/scaleway/terraform/main.tf:31`, et `examples/stacks/scaleway/main.tf:24` épingle la même |
| provider `outscale/outscale` | `~> 1.7`, une contrainte | `tools/conformance/outscale/terraform/main.tf:19`, et `examples/stacks/outscale/main.tf:27` |
| chaîne Go | 1.26.6 | `mise.toml:4` et `.github/workflows/conformance.yml:57` |

Terraform et OpenTofu pilotent chacun les deux contraintes de provider
ci-dessus. C'est la table que le README générait déjà à ce tag sous
`<!-- clients:start -->`, augmentée de ses sources ; elle n'avait simplement
jamais été portée dans le corps de la release, là où un consommateur qui choisit
un pin la rencontrerait.

Deux choses que cette table ne peut pas dire, et qu'elle ne dit pas :

- **La version du provider Outscale réellement exécutée n'est pas récupérable
  depuis ce dépôt.** La fixture énonce `~> 1.7`, aucun fichier de lock n'est
  versionné à côté, et la résolution a eu lieu sur le runner. La contrainte est
  un plancher documenté plutôt qu'un oubli, la génération 1.7+ lisant son
  chemin d'endpoint depuis la valeur là où la 1.1.x l'ajoute elle-même, et
  pointer l'émulateur sur la mauvaise génération coûte six minutes de timeout
  plutôt qu'une erreur. Mais ce qu'un consommateur reçoit de nous ici est un
  intervalle, pas un nombre.
- **Aucune version du provider Terraform Exoscale n'est revendiquée, parce
  qu'aucune n'a été prouvée.** `examples/stacks/exoscale/main.tf` n'en déclare
  aucune, et aucun job de CI ne le fait tourner : le provider Exoscale publié
  compile `.exoscale.com` dans l'un de ses deux clients et ne peut pas être
  pointé sur un émulateur local (exoscale/terraform-provider-exoscale#573),
  donc un apply se scinde entre l'émulateur et un compte payant. La preuve par
  client du pack Exoscale dans cette version, c'est le CLI `exo` à la version
  ci-dessus, et rien d'autre.

`mise.toml` épingle la chaîne d'outils et aucun client : ce n'est donc pas une
source pour cette table.

### Ajouté

- **Une raison de refus modifiée dans un pack atteint désormais l'artefact
  versionné, ou le gate le dit** (#298). `feint coverage --artefact` compare
  `coverage/<provider>-coverage.json` avec ce que le pack déclare aujourd'hui,
  statuts et raisons de refus, et sort en 2 au moindre écart ; `mise run
  drift:check` le passe pour les trois providers, et
  `TestTheCommittedArtefactCarriesWhatThePacksDeclare` échoue sur le même
  écart dans `mise run check`. Cas mesuré : la raison réécrite par #260 est
  restée périmée dans l'artefact pendant quatre jours, 67 occurrences, pendant
  que `drift:check` comparait noms et statuts et que `docs:check` régénérait
  le README depuis le même fichier périmé : deux gates d'accord entre eux et
  en désaccord avec le code. Surface CLI v3 : un drapeau ajouté, rien de
  déplacé ni retiré.

- **Le stockage bloc Exoscale** (#12). Treize opérations — volumes, snapshots,
  le redimensionnement et la chaîne d'attachement — pilotées par le CLI `exo`
  dans la suite de conformance. Chaque nombre qu'un volume publie dit que le
  stockage ne contient aucun octet, et la section de `docs/limits.md` qui
  l'énonce a été livrée avec les routes.

- **Les instance pools Exoscale** (#232). Une écriture qui déplace plusieurs
  machines : le pool crée, redimensionne et évince par le même cycle de vie
  que l'instance seule, et la suite de conformance le pilote avec le CLI
  officiel.

- **Un réseau privé Exoscale est une plage, et un attachement y prend un
  bail** (#161). Les adresses sortent du bloc déclaré au lieu d'être renvoyées
  en écho, donc deux attachements ne peuvent pas revendiquer la même.

- **Les colonnes non triées d'Exoscale et d'Outscale ont été décidées**
  (#173, #198). Vingt-quatre opérations Exoscale et sept Outscale ont quitté
  la colonne par les deux seules sorties possibles : servies avec un client
  pour le prouver, ou déclinées nommément avec la raison dans le pack.

- **Chaque opération montée est pilotée par un vrai client, ou dit pourquoi
  pas** (#174). Les quarante-deux dernières ont soit gagné un client dans la
  suite de conformance, soit déclarent, à la route, pourquoi aucun client
  officiel ne les atteint (`Route.Undriven`) ; le bandeau du README compte les
  deux séparément, et `TestEveryUndrivenOperationSaysWhy` fait échouer une
  raison qui survit à sa cause.

- **Une release est mesurée contre ses consommateurs avant d'être taguée**
  (#170). `mise run compat:check` reconstruit la release précédente depuis
  l'historique de ce dépôt, exécute contre les deux binaires des expressions
  qu'un consommateur aurait légitimement pu écrire, et refuse le tag sur tout
  verdict silencieusement faux non consigné. Contre la 0.8 elle en a trouvé
  deux, toutes deux la lecture de `probed` comme booléen ; elles sont
  consignées dans `tools/compat/accepted.json` avec la raison — un
  consommateur de la 0.8 ne pouvait pas vérifier un signal qui n'existait pas
  encore — et `RELEASING.md` porte le tableau.

- **Un test Go peut demander un cloud, et la CI aussi** (#247, #245, #244,
  #246, #251). `feinttest.Start(t)` démarre l'image publiée et rend
  l'endpoint, sans aucune dépendance — délibérément pas testcontainers, et son
  commentaire de paquet dit pourquoi. `stephrobert/setup-feint@v1` installe le
  binaire publié, vérifie sa somme de contrôle avant de l'exécuter, et attend
  que l'émulateur réponde ; le miroir Marketplace est gardé contre la copie de
  ce dépôt. `examples/` a gagné le job GitHub Actions, le modèle GitLab
  `services:`, le fichier compose et une stack de plateforme Exoscale — et les
  stacks Scaleway et Outscale s'appliquent désormais contre l'image que la
  release construit, à chaque pull request.

- **Deux enregistrements, sur la page et non dans le dépôt** (#252). Ce que ce
  projet a de plus convaincant dure quarante-cinq secondes : une API cloud qui
  répond à un client officiel sans qu'aucun compte existe derrière. Le premier
  montre le binaire sur un poste, le second un job GitHub Actions qui tire
  l'image — les deux portes, chacune à côté de l'extrait qui la concerne, dans
  les deux README. Produits par `mise run demo` depuis des bandes versionnées,
  de sorte qu'une commande qui casse casse la vidéo, et `tools/demo/ci.sh`
  éprouve la séquence avant que `vhs` ne la filme. À voir ici :
  [le chemin CI](https://github.com/stephrobert/feint/blob/main/docs/assets/ci.gif),
  [le chemin poste de travail](https://github.com/stephrobert/feint/blob/main/docs/assets/quickstart.gif).

- **L'émulateur construit ses propres images de machines, et elles portent un
  démon ssh** (#203). Aucune image du serveur amont n'en a : mesuré sur
  `images:ubuntu/24.04`, `ubuntu/24.04/cloud`, `debian/12/cloud` et
  `alpine/3.21/cloud`, chacune regardée deux fois (dès que le conteneur répond,
  puis après `cloud-init status --wait`), et les quatre ont répondu ABSENT avec
  rien à l'écoute sur le port 22. Une machine en installait donc un au premier
  démarrage, ce qui exigeait une sortie internet, donc du NAT, donc de placer
  toute machine sur un pont géré. Et ce pont est la seconde adresse, publiée par
  aucune API, qu'un serveur Scaleway portait ici et ne porte pas sur le vrai
  cloud (#202, mesuré contre de vrais comptes Scaleway et Exoscale : une adresse
  chacun, jamais deux). Une vraie image cloud embarque le démon : c'est donc la
  forme fidèle et non un contournement.

  `feint images` construit les cinq (ubuntu 24.04 et 22.04, debian 12, alpine
  3.21, almalinux 9, une par famille de gabarit cloud-init), `feint images
  --check` rapporte ce qui manque et rend 2, et `feint doctor` les nomme sans
  jamais construire : une construction démarre un conteneur et prend des minutes,
  effet de bord que ce projet demande au lieu de l'exécuter. Le pilote préfère
  une image construite et **annonce** son repli vers l'amont plutôt que de
  dégrader en silence.

  `tools/images/verify.sh` tient ce qui compte, et c'est plus dur que « openssh
  est installé » : chaque machine reçoit une NIC routée sur 203.0.113.0/24, que
  rien ne route et que rien ne masquerade, donc elle n'a aucun chemin vers un
  dépôt de paquets. Vingt contrôles sur les cinq images : réponse sur le port 22,
  aucune sortie, **exactement une adresse**, et deux machines issues d'une même
  image portant deux clés d'hôte différentes.

  La surface CLI gagne un verbe, donc `cliSurfaceVersion` passe de 1 à 2. Rien de
  ce dont un consommateur dépendait n'a changé de forme.

- **Le contrôle de contrat regarde désormais dans le sens de l'omission, et le
  gate de conformance échoue sur ce qu'il trouve** (#88). Le gate attrapait un
  champ que l'émulateur invente et ne voyait pas celui qu'il oublie : un champ
  absent ne viole un schéma que si le fournisseur l'a déclaré `required`, ce
  que Scaleway fait sur 9 % de ses schémas. Les vingt champs manquants de
  ReadVms, et l'omission de ReadImages qui faisait s'effondrer le provider
  Terraform (#86), sont donc restés verts jusqu'à ce qu'un enregistrement d'un
  vrai compte les couvre par hasard, et 45 opérations sur 231 seulement ont un
  tel enregistrement. Chaque réponse provoquée par une exécution de conformance
  est désormais aussi tenue à la *présence* de chaque champ que la description
  d'API du fournisseur déclare (le document même dont les SDK sont générés,
  `contract.ResponseFields`), sur les objets peuplés que créent les vrais
  clients. `/_feint/conformance` publie le verdict sous `fields` (la charge
  utile passe en version de schéma 3, additive) et
  `tools/conformance/score.sh` échoue sur un champ manquant comme il échoue sur
  un champ inventé. Un champ qu'un pack ne sert sciemment pas s'excuse par le
  même `DeclinedFields()` que lit le gate de formes, imprimé avec sa raison ;
  un decline dont le champ est servi dans l'exécution même échoue comme périmé,
  pour que la liste des excuses ne pourrisse pas.

  *Sur les objets qu'un client a pilotés*, et c'est une règle plutôt qu'une
  tournure : les réponses de la sonde ne cautionnent rien ici. La CI l'a prouvé
  dès la première exécution, puisque la jambe `probe` ne pilote aucun client :
  chaque objet y est l'objet minimal que construit le semis, et le gate a
  accusé `ReadVms` d'omettre `PublicIp`, `Tags` et `UserData`, des champs qui
  n'existent que sur une machine configurée par un utilisateur. C'est la
  frontière que #163 avait déjà tracée pour le rapport des champs non lus : le
  trafic synthétique ne déplace aucun chiffre visible d'un client.

- **Ce dont la CI a le droit de dépendre est gelé par un test, pas par une
  phrase** (#132). Les formes de `/_feint/health`, `/_feint/routes`,
  `/_feint/conformance` et `/_feint/trace`, les verbes et drapeaux du CLI et
  les codes de sortie (0 ok, 1 erreur, 2 dérive) ont désormais chacun une
  fixture versionnée (l'arbre des champs, jamais une valeur), comparée par
  `go test` sur chaque pull request. Les trois charges utiles objet gagnent un
  champ `schema_version` (c'est la seule forme de réponse qu'un client peut
  observer changer : une clé nouvelle, additive) pour qu'un pipeline puisse s'y
  brancher ; ce qui garde le champ honnête est le gate, qui refuse un
  changement de fixture sans mouvement de la version déclarée. La
  régénération (`mise run frozen:update`) ajoute à l'historique de la fixture
  et ne réécrit jamais une entrée. Chaque garde a été falsifiée dans une copie
  hors dépôt : cinq mutations, cinq tests nommés qui mordent, et le changement
  volontaire (forme, fixture et version ensemble) qui passe. La procédure pour
  changer une surface gelée à dessein est dans RELEASING.fr.md (« Surfaces
  gelées »).

- **La sonde sème ce dont elle a besoin, si bien qu'un refus est un verdict et
  non une pénurie** (#163). Avant de sonder une opération, la sonde fait
  désormais exister ce que cette opération réclame, à partir du schéma de
  requête du contrat lui-même et des ressources qu'elle a créées plus tôt dans
  la même exécution : les créations sont ordonnées producteurs d'abord, les
  lectures passent deux fois autour, et le démontage reflète le semis. Aucun
  identifiant n'est inventé : chacun vient d'une création réelle contre
  l'émulateur, ce qui est précisément l'origine des 404 d'avant. Face à
  l'artefact qu'il remplace, tous deux régénérés de la même façon :
  `probed: response` **85 → 204**, `refusal` **106 → 4**, `none` **40 → 23**.
  102 des refus et 17 des opérations que la sonde n'atteignait pas valident
  désormais une forme de succès ; rien n'a reculé. L'axe contrat suit,
  **181 → 207 propres**, car une opération qui n'allait jamais au-delà d'un
  refus n'avait aucun corps de succès à contrôler. `driven`, `dataplane`,
  `behaviour` et `negative` ne bougent pas, ce qui est la forme attendue : le
  semis déplace du trafic synthétique, rien de ce qu'un client pilote.

  Ce qui refuse encore est nommé plutôt que caché, puisque tout l'objet de #156
  était de cesser de compter une arrivée comme une preuve. Quatre opérations :
  `get-deploy-target` (le pack sert un inventaire vide et n'en crée jamais,
  `catalog.go` l'écrit en toutes lettres), `update-private-network-instance-ip`,
  `LinkPublicIp` et `UnlinkPublicIp`, chacune réclamant une instance qu'une
  sonde synthétique ne démarre pas. Vingt-trois ne sont jamais sondées : vingt
  ne déclarent aucun schéma de réponse, il n'y a donc aucune forme de succès à
  valider et les appeler ne prouverait que leur réponse ; les trois dernières
  prennent un paramètre de chemin qui n'est pas un identifiant, à savoir
  `{entity}` pour un quota, `{field}` pour la remise à zéro d'un champ et
  `{key}` pour une clé de user data.

### Modifié

- **La moitié « en place » de quatre ressources Outscale est servie** (#172,
  première tranche). La création, la lecture et la suppression des Nets, Subnets,
  NIC et liens de table de routage étaient pilotées par la fixture du provider
  depuis le premier jour ; le changement qu'un **second** `terraform apply`
  produit n'était pas servi du tout, si bien qu'un plan modifiant une ressource
  que cet émulateur avait créée mourait sur une opération dont personne n'avait
  décidé. `UpdateNet`, `UpdateSubnet`, `UpdateNic` et `UpdateRouteTableLink`
  répondent désormais, et la colonne non triée d'Outscale passe de 18 à 14.

  Chaque forme de requête est lue dans `contracts/outscale.json` plutôt que
  rappelée de mémoire. Deux règles que les tests tiennent : **absent n'est pas
  faux** (`MapPublicIpOnLaunch` est requis, et lire un champ manquant comme
  `false` éteindrait le drapeau à chaque appel qui l'oublie) et **une mise à jour
  n'écrit que ce qui est envoyé**, puisque Terraform n'envoie que les attributs
  qui changent et que réinitialiser le reste répond à une requête que personne
  n'a faite.

  Les quatre sont **pilotées par le vrai provider Terraform**, ce qui a demandé
  de lire son schéma plutôt que de deviner : `outscale_net.dhcp_options_set_id`
  est *computed* et n'envoie jamais `UpdateNet` (c'est `outscale_net_attributes`
  qui le fait), et `outscale_route_table_link` ne peut pas être re-pointée du
  tout, ses attributs forçant un remplacement, si bien que l'appel vient de
  `outscale_main_route_table_link`. Deux suppositions, deux fois fausses, toutes
  deux corrigées par la source du provider.

  Les piloter a révélé deux défauts dans les handlers, que quatre tests
  unitaires et une falsification à quatre mutations avaient tous laissés passer.
  Un lien déplacé était reconstruit avec `Main: false` et un `SubnetId` inventé,
  laissant un Net **sans table de routage principale** : le provider le relit en
  filtrant sur `LinkRouteTableMain=true` et ne trouvait rien, juste après un 200.
  Et la liste dont il était retiré était raccourcie par
  `append(links[:i], links[i+1:]...)`, qui mute un tableau que le store partage
  encore jusqu'au `Commit`. Le lien voyage désormais entier, copié plutôt que
  reconstruit. Seul le vrai client a vu l'un comme l'autre.

- **Le cycle de vie des options DHCP d'Outscale est servi** (#172, deuxième
  tranche). `CreateDhcpOptions` et `DeleteDhcpOptions` rejoignent la lecture
  déjà servie, et la colonne non triée passe de 14 à 11 ; la troisième
  opération du lot, `CheckAuthentication`, est déclinée avec la famille IAM :
  l'émulateur accepte toute crédential à dessein (SECURITY.md), donc un verdict
  de validité sur un couple login/mot de passe décrirait une authentification
  qu'il n'effectue jamais.

  La suppression porte les deux refus que leur document énonce : le jeu par
  défaut du compte ne se supprime pas, pas plus qu'un jeu qu'un Net porte
  encore. Le détachement que leur provider effectue avant une suppression a
  fait sortir deux comportements de plus du document vers le pack :
  `UpdateNet` accepte le **mot-clé `default`** (résolu vers le jeu du compte,
  puisqu'un Net porte toujours un identifiant `dopt-`), et `ReadNets` répond au
  **filtre `DhcpOptionsSetIds`** que parcourt son `getAttachedDHCPs`, un filtre
  que le pack refusait jusque-là à dessein.

  Cela ferme la preuve que la première tranche avait notée comme due :
  `outscale_net_attributes` ne pouvait pointer un Net que vers son propre jeu
  par défaut, si bien que rien n'affirmait qu'un jeu *différent* était retenu.
  La fixture crée désormais un second jeu avec `outscale_dhcp_option`, y pointe
  le Net, et la suite demande à l'émulateur (jamais au fichier d'état) que le
  Net le porte, qu'il diffère du jeu par défaut, et que la séquence
  détacher-puis-supprimer du destroy laisse le jeu par défaut seul debout.
- **La règle de fraîcheur du registre de preuves devient un contrôle** (#171).
  « Supprimer une assertion de conformance rétrograde les opérations qu'elle
  prouvait » était écrit deux fois, dans `internal/cli/evidence.go` et dans
  `mise.toml`, et tenu par rien : `grep -rn demotes --include='*_test.go'` ne
  rendait rien. Ce critère est ce qui sépare un registre d'un record absolu, et
  tous les chiffres publiés par ce projet reposent dessus.

  `coverage/evidence.json` passe en **version 3** et gagne `generated_from` : les
  empreintes des contrats, des enregistrements et des suites de conformance. Ce
  ne sont pas des métadonnées posées à côté du registre, elles **gardent la
  jointure**. Deux passes fusionnent en prenant la réponse la plus forte sur
  chaque axe, ce qui n'est sûr que tant que les deux lisent les mêmes entrées :
  une passe produite depuis d'autres est désormais refusée en nommant lesquelles.
  Retirez une assertion d'une suite et l'empreinte des suites bouge, ce qui rend
  le registre précédent injoignable, ce qui est la phrase ci-dessus, appliquée.
  Des empreintes des entrées plutôt qu'un SHA git : reproductibles depuis un
  checkout, encore une réponse sur un arbre sale, et elles répondent à « les
  entrées ont-elles bougé ? » plutôt qu'à « quel commit était sorti ? ».

  Deux défauts ont été trouvés en corrigeant celui-là, de la même famille.
  `runtimesLost` répondait « rien de perdu » à toute erreur de lecture, si bien
  que le changement de version aurait désarmé le garde de non-régression au
  moment précis où il compte le plus ; un fichier absent et un fichier illisible
  sont désormais deux réponses distinctes. Et la jointure construisait un
  registre neuf sans reporter la provenance, si bien que tout artefact régénéré
  portait trois empreintes vides, qui se comparent égales à trois empreintes
  vides : le nouveau gate aurait tout accepté en ayant l'apparence d'un contrôle.
  Les tests unitaires passaient d'un bout à l'autre ; c'est la lecture du fichier
  que l'outil venait d'écrire qui l'a trouvé.

- **`feint stop` dit ce qu'il s'apprête à jeter** (#182). Le store est en
  mémoire et `docs/limits.md` l'énonce dans une table de cycle de vie, mais cette
  phrase vit sur une page qu'un utilisateur lit **après** s'être fait avoir :
  quatre ressources existaient, `stop` a dit `stopped`, et rien d'autre.
  `restart` est le cas le plus aigu, puisqu'un opérateur y recourt en pleine
  session et le paie de tout son jeu d'essai.

  Désormais une ligne sur stderr, avant le signal, qui nomme le compte et la
  sortie de secours : ``discarding 4 resource(s) (started without --state);
  `feint snapshot save <name>` before stopping would have kept them``. Jamais une
  question, jamais un refus, puisque la CI pilote `stop` et que ses codes de
  sortie sont une surface gelée. Elle se tait quand `--state` est enregistré,
  quand le store est vide, et quand l'instance ne répond plus : un avertissement
  à chaque arrêt sain est le motif que l'on apprend à ignorer. Le compte vient de
  l'endpoint que `status` lit déjà, donc les deux ne peuvent pas diverger.

- **Un client mal pointé s'entend dire de quel côté est l'erreur** (#179). Le
  premier contact est le seul moment où un utilisateur ne peut pas encore
  distinguer un émulateur cassé d'un pointage cassé, et les trois pièges que le
  README documente répondaient tous d'une façon qui le poussait vers la mauvaise
  conclusion. Le pire était confiant et faux : `POST /api/v1/api/v1/CreateVms`
  répondait « feint does not serve api/v1/CreateVms », alors que `CreateVms`
  **est** servie. Une équipe dont le premier appel `oapi-cli` lit cela en conclut
  que la table de couverture ment, alors que son endpoint portait le préfixe et
  que le CLI l'a ajouté une seconde fois. Les deux autres répondaient
  `404 page not found`, la page de `net/http`, qui ne dit rien d'aucun des deux
  côtés.

  Les trois restent en 404 : la requête est toujours refusée, seul le refus se
  met à dire la vérité. Le préfixe doublé et le `/api/latest/` déprécié sont
  traités par le pack Outscale, dans son enveloppe d'erreur, pour qu'un client
  décode toujours une erreur d'API. Le troisième est traité par le noyau et il
  est **dérivé, pas déclaré** : la table des routes montées connaît déjà chaque
  préfixe que ce processus sert, donc un chemin qui redevient une route montée
  une fois un préfixe remis devant est une erreur de pointage, et l'émulateur
  nomme ce préfixe. Aucun fournisseur n'est nommé dans ce code, ce qui est
  précisément pourquoi il marchera pour un quatrième pack que personne n'a
  écrit.

  L'indice atteint aussi le journal, parce que certains CLI ne font jamais
  remonter un corps de 404. Le chemin qu'il répète est filtré par liste blanche
  à l'entrée plutôt qu'échappé au rendu, l'ordre que ce dépôt énonce pour
  produire du texte depuis une entrée client, et un chemin hors de cette liste
  reçoit le 404 nu.

- **Une capacité est vérifiée contre l'hôte avant d'être publiée** (#181).
  `NewIncusOVN` posait `OVN = true` et `isolation: true` suivait, quoi que
  l'hôte sache faire : il n'existait qu'un seul `Available` pour les trois modes
  Incus, et il lançait `incus list`, si bien que sur tout hôte dont le démon
  répond, le pilote OVN se déclarait disponible. `internal/cli/cli.go` portait
  un commentaire garantissant un repli qui ne pouvait donc jamais se déclencher :
  la classe de défaut que CLAUDE.md nomme, posée sur la ligne qui choisit ce qui
  tourne sur la machine de l'opérateur.

  Mesuré sur un hôte Incus 7.2 sans aucun câblage OVN : `--vm auto` choisissait
  `incus-ovn` et `/_feint/health` publiait `isolation: true`, jusqu'à ce que la
  première création de réseau échoue en disant au client que le bloc d'adresses
  était déjà pris. Une capacité fausse est strictement pire qu'aucune, puisque
  ce projet envoie tout consommateur vers `capabilities.isolation` plutôt que
  vers un nom de mode.

  `Verify` interroge désormais l'hôte, une fois, au démarrage, par deux lectures
  qui ne créent rien : `network.ovn.northbound_connection` pour l'isolation, et
  la version du démon contre le plancher 6.0.4 pour le pare-feu, ce même
  plancher que cite `feint doctor`, déplacé dans `internal/core/machine` pour
  que les deux ne puissent plus diverger. Sur ce même hôte, `auto` affiche
  maintenant `incus-ovn passed over` avec la raison et retombe sur le pont qui
  fonctionne ; `--vm incus-ovn` demandé par son nom refuse au démarrage en
  nommant la moitié manquante, code 1. Une version illisible conserve la
  capacité au lieu de la perdre : un manque de diagnostic ne doit pas devenir
  une capacité perdue.

  La limite est énoncée plutôt que passée sous silence : un northbound câblé est
  nécessaire et non suffisant, donc un hôte dont l'**uplink** est mal configuré
  publie encore l'isolation et échoue encore à la première création.
  `docs/install.md` le dit, et dit pourquoi sa propre preuve est un vrai
  `CreateSubnet` plutôt que l'endpoint.

- **`/_feint/health` passe en version de schéma 2 et dit quels packs livrent
  réellement une capacité** (#180). `(*Incus).Capabilities()` déclarait
  `firewall: true` dans tous les modes, un seul jeu pour tout le processus, et
  ce projet demande à un consommateur de se brancher sur cette capacité plutôt
  que sur un nom de mode. Or seul `internal/providers/scaleway/firewall.go`
  remettait une règle au runtime : un groupe de sécurité Outscale ou Exoscale
  était créé, renvoyé, puis réconcilié sur rien, pendant que le même processus
  répondait `firewall: true`. Un utilisateur suivant ce conseil sondait un port
  qu'un groupe en refus par défaut aurait dû fermer, le trouvait ouvert, et on
  lui avait dit que le pare-feu était livré. Un 200 qui ment.

  La charge utile gagne `enforced`, indexée par capacité, nommant les packs qui
  lui confient du travail : `capabilities.firewall` dit ce que le runtime **peut**
  faire, `enforced.firewall` dit qui le lui demande, et le contrôle honnête est
  la conjonction des deux. Un pack le déclare en implémentant
  `emulator.FirewallEnforcer` ; celui qui ne le fait pas n'apparaît nulle part,
  suivant la règle constante voulant qu'une capacité non déclarée vaille absente.
  La documentation seule ne pouvait pas refermer cela : l'affirmation est lue
  depuis un endpoint, par un programme, au moment où elle compte.

  `TestEveryPackThatWiresTheFirewallSaysSo` compare chaque déclaration à la
  présence de `machine.Firewaller` dans les sources non-test du pack, si bien que
  l'affirmation ne peut plus diverger du code, dans un sens comme dans l'autre.
  Le README nomme Scaleway là où il ne nommait aucun fournisseur, et
  `docs/limits.md` énonce le manque pour les deux packs qui ne le livrent pas,
  dont l'un n'avait aucune phrase nulle part.

- **`/_feint/conformance` passe en version de schéma 3**, en deux étapes additives prises pendant cette version. `evidence.*[].probed`
  était un booléen et vaut désormais `response`, `refusal` ou `none` (#156). Un
  consommateur testant `probed === true` lirait une chaîne toujours vraie et
  compterait chaque refus comme un succès — la surestimation que #156 supprime,
  réapparue une couche plus loin. Le changement de version est ce qui lui permet
  de s'en apercevoir.

### Corrigé

- **Une subregion Outscale est une donnée que les lectures restituent, pas une
  constante** (#268, #269). Une Vm placée en `eu-west-2b` se relisait
  `eu-west-2a`, donc le second plan d'une stack étrangère ne convergeait
  jamais ; et `ReadSubregions` répondait un élément là où une data source en
  indexait deux. Les deux moitiés restituent désormais ce qui a été écrit,
  attesté par deux des quinze stacks du relevé
  (`examples/stacks/surveyed.md`).

- **Un réseau privé Scaleway publie son /64 IPv6, double pile comme
  l'upstream** (#270). Une stack lisant `one(pn.ipv6_subnets).subnet` n'en
  trouvait aucun et bloquait jusqu'à son propre `terraform destroy` sur le
  null ; le réseau porte désormais le /64 que le vrai cloud lui donne.

- **Un paramètre de requête déclaré chez Exoscale est servi ou refusé, jamais
  ignoré** (#271). `GET /v2/template?visibility=private` répondait le
  catalogue public, parce que le handler jetait sa requête — et la même
  signature pesait sur quatre autres opérations dont le contrat déclare des
  filtres. Les cinq lisent désormais ce que leur opération déclare,
  `TestDeclaredQueryParametersAreRead` tient la règle pour chaque route
  montée, et le seul refus délibéré (`labels`, un filtre dont l'upstream
  n'énonce jamais le format) répond 400 et est documenté dans
  `docs/limits.md`.

- **Une route Outscale atteint un Net peering, et une NIC garde ses tags**
  (#249, #250). Deux défauts que deux stacks réalistes ont fait apparaître en
  une heure, tous deux invisibles pour la suite de conformance du moment ; les
  correctifs ont tenu sous la configuration d'un inconnu dans le relevé #262.

- **Quinze stacks Terraform écrites par des gens qui n'ont jamais vu ce dépôt ont
  été appliquées contre l'émulateur** — cinq par fournisseur, choisies pour leur
  taille et non pour leur commodité, chacune consignée avec son dépôt, son commit
  et sa licence dans `examples/stacks/surveyed.md` (#262). **Six n'ont demandé
  aucune modification. Cinq n'en ont demandé qu'une, et dans tous les cas la cause
  était le provider tiers ou l'âge de la stack, jamais Feint.**

  Ce que cela prouve : des configurations écrites indépendamment de ce projet
  peuvent lui être pointées et fonctionner. Ce que cela ne prouve pas : que les
  produits que ces stacks n'utilisent *pas* fonctionnent, ni que quinze dépôts
  représentent un écosystème. Le registre sépare strictement les produits déclinés
  — SKS, LBU, DBaaS, stockage objet, Kapsule — des défauts de Feint, et les compte
  par fournisseur : un `501` qui nomme une route non servie est le comportement
  voulu, un `200` faux ne l'est pas.

  L'exercice a trouvé quatre défauts, tous de la seconde espèce. Il a aussi produit
  une contre-preuve qui vaut autant : une stack de 95 ressources, trois VPC, un
  peering et des routes au travers s'applique, se replanifie à vide et se détruit.

- **Le `BootMode`, la `Performance` et le `VmInitiatedShutdownBehavior` d'une Vm
  font l'aller-retour** (#276). Acceptés avec un 200 et relus comme des constantes
  du pack — la forme de #268, trois champs plus loin, et le même plan qui ne
  converge jamais. Le balayage qui a suivi a aussi corrigé
  `ShutdownBehaviorConfiguration`, dont la constante contredisait les valeurs par
  défaut documentées par le SDK lui-même. Ce que l'émulateur répète sans l'imposer
  est écrit dans `docs/limits.md`.

- **Dix-huit opérations de liste Scaleway honorent les 72 paramètres de requête
  qu'elles déclarent** (#277). La règle de #271 ne tenait que là où un handler ne
  lisait *rien* ; celles-ci en lisaient certains et en laissaient tomber d'autres,
  si bien que `order_by=…_desc` répondait en ordre croissant. Chaque `order_by`
  s'applique désormais avec le défaut documenté par le SDK pour son opération — un
  simple `scw instance server list` répond du plus récent au plus ancien, comme la
  vraie API. Ce qui ne peut pas être servi honnêtement est refusé par un 400 qui
  nomme le paramètre, et consigné dans `docs/limits.md`.

- **Une règle, une route ou une étiquette acquittée survit à sa jumelle
  concurrente** (#289, #295). Deux `CreateSecurityGroupRule` sur un même groupe
  Outscale répondaient chacun 200 et une règle n'était jamais stockée — huit
  acquittements, cinq règles — après quoi `terraform destroy` mourait sur le
  fantôme. La moitié « collections » puis la moitié « champs scalaires » (mesurée à
  11 essais sur 200) tiennent maintenant lecture, contrôle et écriture dans une
  seule section critique. Le barrage partagé qui aurait dû l'attraper existait :
  sa doctrine cadrait la propriété comme *un champ par écrivain*, si bien qu'aucun
  pack ne pilotait de collection. Elle nomme désormais les deux formes.

- **La région Outscale est une donnée, et ses subregions la suivent** (#290).
  Toutes les régions Outscale sont servies par la même API — une région est une
  propriété de l'endpoint auquel un client est pointé, pas de la surface — donc
  figer une seule région refusait les stacks écrites pour une autre, dont la région
  SecNumCloud que vise le public de ce projet. `FEINT_OUTSCALE_REGION` la
  sélectionne ; ne rien configurer conserve le comportement précédent. Lire la
  publication plutôt que la table précédente l'a aussi corrigée : **`eu-west-2`
  publie aujourd'hui trois subregions (PAR1, PAR4, PAR7), pas deux.**

- **`UpdateNic` sert `DeleteOnVmDeletion` sur une NIC que ce pack a lui-même
  attachée** (#299). Le handler cherchait une carte d'attachement que rien
  n'écrivait, si bien que la seule requête prévue en amont pour basculer le drapeau
  répondait *400, non attachée* sur une interface que l'émulateur venait
  d'attacher avec un 200 — et `outscale_nic_link` envoie exactement cette requête.

- **Les dernières opérations non triées du dépôt ont été décidées** (#300). Les
  onze opérations NLB d'Exoscale et les seize du VPC étaient les seules entrées
  `unknown` restantes des trois fournisseurs ; les deux familles sont désormais
  déclinées par leur nom, avec des raisons ancrées sur des faits plutôt que sur des
  intentions — `healthcheck-status` est un verdict par backend dont l'énumération
  n'a pas de troisième valeur, donc un émulateur qui ne sonde rien devrait en
  inventer un.

- **L'`AvailableIpsCount` d'un subnet Outscale est lu depuis le pool qui
  distribue les adresses** (#217), le drapeau de protection d'un serveur
  Scaleway gouverne exactement les actions qu'il a été mesuré gouverner
  (#212), et une ressource exclusive — une adresse, un attachement de volume —
  a un seul propriétaire vivant, que la couche partagée impose désormais aux
  trois packs d'un coup (#213, #214, #215).

- **La matrice des clients crédite chaque fournisseur contre lequel la CI pilote
  un client** (#155). La colonne « Emulated provider » du tableau du README
  était une constante du générateur, sous un marqueur disant « Généré … ne pas
  modifier à la main », et elle attribuait Terraform au seul Scaleway alors que
  `conformance.yml` lançait `tools/conformance/outscale/terraform.sh` sur chaque
  pull request, sous les deux branches terraform et opentofu. Généré n'est pas
  dérivé : la colonne est désormais lue par le même balayage du workflow que la
  table de statut, et chaque ligne qui répond au travers d'un provider Terraform
  affiche la contrainte que la fixture de ce fournisseur épingle, lue depuis la
  fixture plutôt que redite ici — redite, elle s'est périmée en une release,
  quand #257 a remplacé le flottant Scaleway par un épinglage exact.
  Sous-estimer une preuve coûte autant que la surestimer :
  un audit externe a recommandé de retirer Terraform de la ligne Outscale du
  README sur la foi de cette colonne, ce qui aurait effacé une suite qui
  applique vingt et une ressources. Un client que la CI pilote et que le
  générateur ne liste pas est maintenant refusé au lieu d'être omis. Trois
  mutations falsifiées dans une copie hors dépôt, trois tests nommés qui
  mordent.

## [0.8.0]

### Corrigé

- **Une assertion de destruction distingue deux pannes** (trouvé en levant une
  réserve sur #152). La suite Terraform Outscale demandait si l'émulateur ne
  détenait **aucun** Net, ce qui n'est vrai que lorsqu'elle en est la seule
  créatrice sur un émulateur neuf. Exécutée sur un banc qu'un autre run avait
  touché, elle annonçait « the destroyed Net still answers » alors que la
  destruction avait parfaitement fonctionné : le message accusait le sujet pour
  l'état du banc.

  Elle demande désormais si le Net **de ce run** a disparu, le nomme quand ce
  n'est pas le cas, et dit franchement quand des ressources d'un autre run
  traînent encore. Falsifié : faire survivre un Net à son propre delete fait
  échouer la suite en nommant l'identifiant.

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

[0.11.0]: https://github.com/stephrobert/feint/releases/tag/v0.11.0
[0.10.0]: https://github.com/stephrobert/feint/releases/tag/v0.10.0
[0.9.0]: https://github.com/stephrobert/feint/releases/tag/v0.9.0
[0.8.0]: https://github.com/stephrobert/feint/releases/tag/v0.8.0
[0.7.3]: https://github.com/stephrobert/feint/releases/tag/v0.7.3
[0.7.2]: https://github.com/stephrobert/feint/releases/tag/v0.7.2
[0.7.1]: https://github.com/stephrobert/feint/releases/tag/v0.7.1
[0.7.0]: https://github.com/stephrobert/feint/releases/tag/v0.7.0
[0.6.0]: https://github.com/stephrobert/feint/releases/tag/v0.6.0
[0.5.0]: https://github.com/stephrobert/feint/releases/tag/v0.5.0
[0.4.1]: https://github.com/stephrobert/feint/releases/tag/v0.4.1
[0.4.0]: https://github.com/stephrobert/feint/releases/tag/v0.4.0
[0.3.3]: https://github.com/stephrobert/feint/releases/tag/v0.3.3
[0.3.2]: https://github.com/stephrobert/feint/releases/tag/v0.3.2
[0.3.1]: https://github.com/stephrobert/feint/releases/tag/v0.3.1
[0.3.0]: https://github.com/stephrobert/feint/releases/tag/v0.3.0
[0.2.0]: https://github.com/stephrobert/feint/releases/tag/v0.2.0
[0.1.0]: https://github.com/stephrobert/feint/releases/tag/v0.1.0
