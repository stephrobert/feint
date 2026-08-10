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

2. **Fusionner dans `main` par une pull request, et attendre une CI verte.** Le
   tag construit depuis ce commit et une release publiée ne se rejoue pas.

3. **Lancer le préflight**, qui rejoue hors ligne tout ce qui doit tenir :

   ```bash
   mise run release:check -- v0.1.0
   ```

   Il vérifie un arbre propre sur `main`, que le tag est libre localement *et* sur
   le dépôt distant, `mise run check`, `mise run drift:check` à 0 — une release
   publiée avec des opérations upstream non triées annonce un chiffre de
   couverture qui n'est pas vrai —, `feint docs --check` à 0, la section du
   CHANGELOG, `coverage/` et `contracts/` commités, et que la conformance est
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

La seule exception qui mérite d'être signalée : **une forme de réponse corrigée
pour correspondre au document du provider est un correctif, pas une rupture**,
même quand un test en aval affirmait la mauvaise. C'est tout le propos du projet,
et un test qui dépendait de l'erreur de l'émulateur mesurait l'émulateur plutôt
que le cloud.
