# Publier une version

**Autre langue :** [English](./RELEASING.md)

Une version, c'est un tag. Il n'y a aucun numéro à incrémenter dans un fichier :
`release.yml` estampille le tag dans le binaire via `-ldflags`, et un binaire
construit autrement retombe sur la version de module que la chaîne d'outils Go y
enregistre. Rien ne peut dériver du tag, parce que rien d'autre ne porte le
numéro.

## Quel numéro

Ce sont les commits qui décident, pas le goût. Les sujets suivent les
[Conventional Commits](https://www.conventionalcommits.org/fr/v1.0.0/), imposés
par le hook `commit-msg` et par le workflow `Commits`, et commitizen en déduit
l'incrément :

```bash
cz bump --dry-run     # p. ex. « bump: version 0.1.0 → 0.2.0 / tag to create: v0.2.0 »
```

`fix` fait bouger le patch, `feat` le mineur, un `!` avant les deux-points marque
une rupture. Avant 1.0, une rupture reste à l'intérieur de `0.x`
(`major_version_zero` dans `pyproject.toml`), ce qui est la règle que la section
Versionnage ci-dessous énonce en prose.

Deux limites délibérées. **La version ne vit que dans les tags git**
(`version_provider = "scm"`) : un fichier qui la porterait serait une seconde
source capable de contredire le tag. Et **le CHANGELOG est écrit, pas généré**
(`update_changelog_on_bump = false`) : son propre en-tête dit quels deux types de
changement méritent une ligne quelle que soit leur taille, et un générateur qui
émettrait chaque sujet enterrerait les deux sous les refactorisations. Donc
commitizen propose le numéro, le préflight vérifie que le tag y correspond, et la
prose reste la vôtre.

## En publier une

1. **Déplacer les entrées `Unreleased`** de [CHANGELOG.md](./CHANGELOG.md) sous un
   nouveau titre `## [X.Y.Z]`. Le workflow de release lit cette section pour le
   corps de la GitHub Release : une entrée qui n'y est pas est une entrée que
   personne téléchargeant un binaire ne verra.

   Puis **`mise run docs:coverage`**, dans le même commit. C'est de ce titre que
   les commandes d'installation de README.md et docs/install.md tirent la version
   qu'elles disent au lecteur de télécharger — épinglée plutôt que `latest`, parce
   qu'une référence mutable ne peut pas être vérifiée contre une empreinte et
   adopte une release le jour de sa publication. Déplacer le titre sans
   régénérer laisse les deux pages une version en retard, et `feint docs --check`
   rend 2 tant que ce n'est pas corrigé : au hook pre-commit, sur la pull request,
   dans le préflight ci-dessous, et une dernière fois dans le workflow de release,
   qui refuse de publier plutôt que de réparer quoi que ce soit.

   Et **`mise run release:surface`**, qui répond à la question dont ce titre
   est le sujet : la section nomme-t-elle ce que cette version se met à servir
   et ce qu'elle cesse de servir. Il compare les `coverage/*-coverage.json`
   versionnés du dernier tag à ceux de cet arbre et rend 2 dès qu'une opération
   qui a changé de camp n'est nommée ni là ni dans
   [tools/release/unnamed.json](./tools/release/unnamed.json). Il affiche les
   opérations manquantes, prêtes à coller. Trois transitions sont exigées :
   nouvellement servie, plus servie, et **un refus retiré** — celle que personne
   ne pense à publier, et celle qu'un consommateur contourne (#326).

2. **Fusionner dans `main` par une pull request, et attendre une CI verte.** Le
   tag construit depuis ce commit et une release publiée ne se rejoue pas.

3. **Lancer le préflight**, qui rejoue hors ligne tout ce qui doit tenir :

   ```bash
   mise run release:check -- v0.1.0
   ```

   Il vérifie un arbre propre sur `main`, que le tag est libre localement *et* sur
   le dépôt distant, `mise run check`, `mise run drift:check` à 0 — une release
   publiée avec des opérations upstream non triées annonce un chiffre de
   couverture qui n'est pas vrai —, `feint docs --check` à 0,
   `mise run release:surface` à 0, la section du CHANGELOG, `coverage/` et `contracts/` commités, et que la conformance est
   verte sur ce commit exact. Il rapporte chaque verdict au lieu de s'arrêter au
   premier, et il affiche les commandes à lancer une fois que tout passe.

   Il ne lance délibérément **pas** les suites de conformance lui-même : elles
   demandent `scw`, `oapi-cli`, `exo` et Terraform installés, et une release
   publiée depuis une machine à laquelle il en manque un sauterait en silence la
   preuve même sur laquelle ce projet repose. Il demande plutôt à la CI si elles
   sont passées.

4. **Taguer et pousser** :

   ```bash
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

5. **Vider `tools/release/unnamed.json`, s'il portait quelque chose.** Ses
   entrées dispensaient des opérations d'être nommées dans la version que vous
   venez de couper, et la fenêtre pour laquelle elles ont été écrites s'est
   fermée avec le tag. Une dispense qui ne dispense rien est refusée — une
   dispense périmée est un contrôle qui a cessé sans bruit de couvrir ce qu'il
   nomme —, donc `mise run release:surface` devient rouge à la poussée suivante
   tant qu'elles n'ont pas été retirées. C'est le triage qu'il demande, et cela
   tient en une suppression. La liste est vide dans le cas ordinaire, ce qui
   fait de cette étape un non-événement la plupart du temps.

C'est pousser le tag qui publie. Cela ne se défait pas discrètement : un tag doit
être supprimé des deux côtés, et une release qui a atteint le monde a été
téléchargée.

## Ce que le tag déclenche

`.github/workflows/release.yml`, sur `v*` :

- construit les binaires `linux` et `darwin` pour `amd64` et `arm64`, tag
  estampillé,
- génère les sommes de contrôle SHA-256 et un SBOM CycloneDX (Syft),
- enregistre la **provenance de build SLSA** pour les binaires et atteste le SBOM,
- signe les sommes de contrôle avec **Cosign sans clé**,
- crée la GitHub Release avec tous les artefacts attachés, dont
  `provenance.intoto.jsonl`.

Ce dernier artefact est attaché exprès et n'est pas redondant avec l'attestation
enregistrée via l'API GitHub : c'est le fichier que cherche le contrôle
*Signed-Releases* d'OpenSSF Scorecard. Ce contrôle note les **cinq releases les
plus récentes**, donc le maximum n'est atteint qu'une fois que cinq releases
consécutives le portent.

## Après le tag : le tap Homebrew

Une étape que le workflow ne prend délibérément pas. Une formule binaire a
besoin de l'URL et de la somme SHA-256 de chaque artefact publié, et ces deux
faits n'existent qu'une fois la release en ligne : soit le workflow les pousse
dans `stephrobert/homebrew-feint`, ce qui demande un jeton inter-dépôt que ce
dépôt ne détient pas, soit quelqu'un les copie et un gate refuse une copie
périmée. Ce projet a déjà refusé la première forme deux fois par écrit (le
miroir Marketplace dans `.github/workflows/workflow-security.yml`, les pages
générées dans `internal/cli/docs_release.go` : *un gate qui refuse est sûr ; un
gate qui répare le dépôt est une seconde porte d'entrée*). C'est donc la
seconde :

```bash
mise run release:formula > Formula/feint.rb   # dans le clone du tap
```

Rien n'y est tapé. `release:formula` récupère le `checksums.txt` signé par
cosign de la release et en dérive le fichier, donc les empreintes du tap sont
celles de la release et non leur recopie. Commiter, pousser, puis

```bash
mise run release:tap
```

répond 0. Il sort en 2 tant que le tap sert autre chose, et
`.github/workflows/tap.yml` le lance chaque jour : un tap laissé derrière par
une release est nommé dans la journée plutôt que deux versions plus tard. Il
n'est pas sur le chemin des pull requests : seul celui qui peut pousser sur le
tap peut en éteindre le rouge, et un gate dont un contributeur ne peut rien
faire est un gate que tout le monde apprend à sauter.

## Vérifier une release

N'importe qui peut vérifier qu'un binaire vient bien du workflow de ce dépôt, et
depuis quel commit :

```bash
gh release download v0.1.0 --repo stephrobert/feint --pattern 'feint-linux-amd64'
gh attestation verify feint-linux-amd64 --repo stephrobert/feint

cosign verify-blob --bundle checksums.txt.cosign.bundle \
  --certificate-identity-regexp 'https://github.com/stephrobert/feint/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum -c checksums.txt --ignore-missing
```

## Ce sur quoi vous pouvez compter, et pour combien de temps

Les surfaces ci-dessous sont tout ce qui est couvert. **Ce qui n'y figure pas ne
porte aucune promesse**, et le dire franchement est le sujet : un émulateur qui
promettrait tout son comportement promettrait une copie mouvante de trois clouds.

| surface | signal | gelée dans |
|---|---|---|
| `/_feint/health` | `schema_version` | `internal/cli/testdata/frozen/health.json` |
| `/_feint/routes` | `schema_version` | `internal/cli/testdata/frozen/routes.json` |
| `/_feint/conformance` | `schema_version` | `internal/cli/testdata/frozen/conformance.json` |
| `/_feint/trace` | `schema_version` | `internal/cli/testdata/frozen/trace.json` |
| les verbes, drapeaux et codes de sortie du CLI | `cliSurfaceVersion` | `internal/cli/testdata/frozen/cli.json` |
| les formats d'état et de snapshot | la version du document | `internal/cli/snapshot.go` |

**Servir davantage de l'API d'un provider n'est jamais cassant**, quelle que soit
l'ampleur du changement. C'est cette asymétrie qui rend la fenêtre tenable : la
surface qui peut casser est petite et énumérée ci-dessus, et tout ce qu'un pack
gagne atterrit en dehors.

### Le préavis

**Une version mineure.** Un champ ou un verbe qui disparaît est déprécié dans une
version et retiré au plus tôt dans la suivante, et les deux événements sont des
lignes du `CHANGELOG.md`. Une dépréciation qui apparaît et disparaît dans la même
version n'en est pas une.

Une version plutôt qu'un nombre généreux, délibérément. C'est un projet 0.x tenu
par une personne, et ce dépôt a déjà mesuré ce que coûte une condition
intenable : l'étiquette *preview* d'Exoscale attendait le tracker de quelqu'un
d'autre et est restée fausse pendant des mois. Une promesse d'une version
mineure, tenue, vaut mieux qu'une plus longue qui sera rompue.

### Le signal

Chaque charge utile ci-dessus porte `schema_version`, et il bouge quand la forme
bouge, ajouts compris, parce que le nombre veut dire *la surface a changé* et non
*la surface a cassé*. `cliSurfaceVersion` fait la même chose pour les verbes, les
drapeaux et les codes de sortie. Un fixture régénéré sans que sa constante suive
fait échouer la CI.

### La sortie de secours

Un consommateur qui lit une version qu'il ne connaît pas doit **s'arrêter plutôt
que deviner**. Les formes sont additives à l'intérieur d'une version, donc une
version inconnue **supérieure** se lit sans risque pour les champs déjà connus ;
une version inconnue **inférieure** signifie que l'émulateur est plus ancien que
ce que le pipeline attend, et que les champs espérés peuvent ne pas exister
encore.

Concrètement, et c'est toute la recette :

```bash
version="$(curl -sf localhost:4599/_feint/conformance | jq -r '.schema_version')"
[ "$version" -ge 3 ] || { echo "feint est plus ancien que ce pipeline n'attend"; exit 1; }
```

Lire un champ sans vérifier est ce qui casse en silence : `probed` est passé de
booléen à chaîne, et un pipeline qui le traitait comme vrai comptait chaque refus
comme un succès. C'est ce chemin que #170 mesure ; cette section est ce contre
quoi il le mesure.

### Ce que la mesure a trouvé, et la frontière qu'elle ne peut pas protéger

`mise run compat:check` reconstruit la release précédente depuis l'historique de
ce dépôt, démarre les deux binaires hors ligne, les amorce à l'identique, et
exécute un jeu d'expressions qu'un consommateur aurait légitimement pu écrire.
Chacune tombe dans exactement une case : compatible, cassée explicitement, ou
**silencieusement fausse**. Un seul verdict silencieusement faux non consigné
refuse le tag, et `tools/release/preflight.sh` l'applique.

Contre la 0.8, elle en a trouvé deux, toutes deux l'expression `probed` sous ses
deux formes naturelles :

| ce que le consommateur a écrit | 0.8 | 0.9 |
|---|---|---|
| `select(.probed == true)` | certaines opérations | **aucune** |
| `select(.probed)` | certaines opérations | **toutes** |

La seconde est la pire : chaque opération se lit désormais comme sondée, refus
compris, c'est-à-dire exactement la surestimation que #156 existait pour
supprimer, réapparue dans le pipeline de quelqu'un d'autre.

**Aucune des deux n'est réparable rétroactivement**, et cela vaut d'être écrit
plutôt que laissé sous un gate vert. `schema_version` est arrivé avec #132,
*après* la sortie de la 0.8. Un consommateur de la 0.8 ne pouvait pas vérifier
un signal qui n'existait pas, donc la recette ci-dessus protège à partir de la
0.9 et pas avant. Les deux constats sont consignés dans
`tools/compat/accepted.json` avec cette raison, et ils s'impriment encore à
chaque exécution — consignés, jamais cachés.

Le gate porte sur la suite : à partir d'ici, un changement de forme fait bouger
le `schema_version` de la surface, ou refuse la release.

## Versionnage

[Semantic Versioning](https://semver.org/lang/fr/). Avant 1.0, la version mineure
bouge sur tout ce qu'un client peut observer.

Ce qui compte comme rupture ici est plus étroit qu'il n'y paraît, et cela mérite
d'être dit parce que toute la surface de ce projet est l'API de quelqu'un d'autre.
**Servir davantage de l'API d'un provider n'est jamais une rupture**, si grand que
soit le changement : un client qui demande une opération qui rendait 404 et qui
fonctionne désormais est un client qui a obtenu ce qu'il a toujours voulu. Ce qui
casse est du côté propre de ce projet — les verbes et drapeaux du CLI, les codes
de sortie, la forme de `/_feint/*`, les formats d'état et de snapshot, et tout
comportement émulé sur lequel un test aurait pu s'appuyer.

### Surfaces gelées

L'essentiel de cette liste n'est plus de la prose (#132). Les formes de
`/_feint/health`, `/_feint/routes`, `/_feint/conformance` et `/_feint/trace`,
les verbes et drapeaux du CLI et les codes de sortie ont chacun une fixture
versionnée sous `internal/cli/testdata/frozen/` (l'arbre des champs, jamais une
valeur), et deux tests les gardent sur chaque pull request :
`TestTheFrozenSurfacesStillMatchTheirFixture` échoue quand une forme bouge sans
sa fixture, et `TestASurfaceChangeDemandsItsVersionBump` échoue quand la fixture
bouge sans la version déclarée. Les trois charges utiles objet servent cette
version dans `schema_version`, pour qu'un consommateur puisse s'y brancher ; le
gate est ce qui empêche le champ de mentir.

Changer une de ces surfaces volontairement tient en quatre étapes, dans un même
commit :

1. changer le code ;
2. `mise run frozen:update` : la nouvelle forme est ajoutée à l'historique de la
   fixture, à la version suivante, sans jamais réécrire une entrée ;
3. incrémenter la constante correspondante : `internal/core/emulator/schema.go`
   pour les charges `/_feint/*`, `cliSurfaceVersion` dans
   `internal/cli/cli.go` pour le CLI. Les tests restent rouges jusqu'à cette
   étape, et c'est le but ;
4. écrire la ligne de CHANGELOG dont l'incrément est le signal. Correctif ou
   rupture, c'est la question ordinaire de cette section ; la fixture prouve
   seulement que la surface a bougé, pas lequel des deux c'était.

Ce qui n'est délibérément pas gelé : la prose du texte d'aide autour des verbes
et drapeaux, les valeurs derrière chaque clé gelée (compteurs, identifiants,
listes qui grandissent quand des routes se montent), et les champs de trace que
seuls certains échanges portent (`unread`, `violations`). Un gel qui attraperait
l'un d'eux rougirait sur des exécutions de routine et serait désarmé dans la
semaine.

Le format de snapshot est celui d'entre eux qui énonce sa propre version.
Depuis #133, un snapshot est
`{"format": "feint-snapshot", "version": N, "resources": [...]}`, et `Restore`
refuse tout ce dont il ne peut pas rendre compte : une autre version, un autre
format, un champ inconnu. Faire bouger `snapshotVersion` dans
`internal/core/store/store.go` est donc une rupture au sens de cette section —
et un fichier écrit par un feint plus ancien échoue bruyamment à la frontière
au lieu de restaurer les trois quarts de lui-même en silence, ce qu'il faisait
avant.

La seule exception qui mérite d'être signalée : **une forme de réponse corrigée
pour correspondre au document du provider est un correctif, pas une rupture**,
même quand un test en aval affirmait la mauvaise. C'est tout le propos du projet,
et un test qui dépendait de l'erreur de l'émulateur mesurait l'émulateur plutôt
que le cloud.
