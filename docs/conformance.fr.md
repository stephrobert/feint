# Le système de conformance

**Autre langue :** [English](./conformance.md)

L'affirmation de ce projet tient en une phrase : *le client officiel ne doit pas
voir la différence*. Cette page est ce qui tient cette phrase honnête : la
chaîne entière, depuis la description d'API du fournisseur jusqu'à un pipeline
qui lit un chiffre dans cet émulateur, et ce que chaque maillon prouve ou ne
prouve pas.

Elle existe parce que la chaîne est désormais assez longue pour qu'aucun gate
seul ne l'explique, et parce qu'un lecteur mérite de savoir quels maillons sont
fermés et lesquels ne le sont pas. Les maillons ouverts sont nommés à la fin,
avec leurs issues. Une page qui ne décrirait que la moitié terminée serait
exactement le défaut que ce projet passe son temps à supprimer.

L'[architecture](architecture.md) décrit l'organisation du code ;
[limits.md](limits.md) décrit ce que l'émulateur ne fait délibérément pas. Cette
page décrit comment quoi que ce soit ici obtient le droit d'être appelé
*prouvé*.

> [!NOTE]
> L'anglais est la source. En cas de désaccord entre les deux pages,
> [la version anglaise](conformance.md) fait foi.

## La chaîne

```text
                      le SDK upstream du fournisseur
                                   │
              scan de surface ─────┴───── baseline      gate de dérive
                                   │
                   artefact de description d'API
                             (contracts/)
                                   │
                 ┌─────────────────┴─────────────────┐
                 │                                   │
        un vrai client pilote              la sonde pilote chaque
     (scw, oapi-cli, exo, Terraform)      route depuis le contrat
                 │                                   │
                 │        enregistrements d'un vrai cloud
                 │                 (shapes/)
                 │                     │             │
                 └─────────┬───────────┴─────────────┘
                           │
                    le registre de preuves
                 (sept axes, jamais additionnés)
                           │
                   /_feint/conformance
                    + schema_version
                           │
                  un consommateur en CI
```

Chaque flèche est un endroit où une affirmation peut se perdre ou s'inventer, et
chacune a son propre contrôle ci-dessous.

## Maillon 1 : l'upstream, mesuré plutôt que suivi

Ce qui distingue ce projet, c'est que **l'API est mesurée, pas suivie à la
main**. Scaleway a ajouté 363 opérations et en a retiré 25 en douze mois :
personne ne suit cela de tête.

- **Le scan de surface** (`internal/drift`) lit le SDK Go officiel du
  fournisseur, généré depuis leur IDL et donc exact. Aucun appel réseau, aucune
  supposition. Il lit tous les fichiers `.go` non-test, pas seulement les
  générés, parce que Scaleway écrit à la main des points d'entrée publics dans
  `*_utils.go` qui délèguent à des méthodes générées non exportées.
- **La baseline** (`coverage/*-baseline.json`) est versionnée. Une opération qui
  apparaît upstream et que personne n'a triée fait échouer la CI avec le code de
  sortie 2.
- **Le tri est binaire et explicite.** Une opération est servie, ou elle est
  dans `Declined()` avec sa raison en commentaire. « Pas encore fait » et « hors
  périmètre » ne sont pas la même chose, et une déclinaison qui ne dit ni l'un
  ni l'autre est une opération non triée déguisée en décision.

Chaque semaine, `.github/workflows/drift.yml` scanne et ouvre une pull request
quand quelque chose a bougé. Le travail humain est le tri.

## Maillon 2 : le contrat, et ce qu'une exécution verte ne prouve pas

`contracts/` porte la description d'API de chaque fournisseur, extraite de leur
propre document. Chaque réponse que l'émulateur écrit avec `--contracts` est
validée contre elle, et une violation fait échouer l'exécution.

Ce contrôle est **unidirectionnel**, et le dire n'est pas une réserve : c'est la
raison d'être du maillon 4. Il attrape un champ que l'émulateur **invente**, et
il ne peut attraper un champ **omis** que là où le fournisseur l'a marqué
`required`, ce que Scaleway fait sur 11 % de ses schémas.
[limits.md](limits.md#what-a-green-contract-run-does-and-does-not-prove) porte le
détail mesuré, y compris pourquoi les trois descriptions ne se valent pas.

## Maillon 3 : deux témoins, et aucun ne remplace l'autre

**Un vrai client** est la seule chose qui puisse prouver l'affirmation,
puisqu'elle porte sur les clients. `tools/conformance/<fournisseur>/` pilote
`scw`, `oapi-cli`, `exo`, Terraform et OpenTofu contre l'émulateur. Ce qu'une
suite affirme est ce qui est prouvé, rien de plus : deux défauts ont porté
`driven` pendant tout le temps où ils étaient faux.

**La sonde** (`internal/probe`) pilote chaque route montée depuis la description
d'API du fournisseur, ce qui est plus large que tout ensemble de cas que
quelqu'un aurait pensé à écrire. Depuis #163 elle **sème** : avant de sonder une
opération, elle fait exister ce que cette opération réclame, depuis le schéma de
requête du contrat et depuis les ressources qu'elle a créées plus tôt dans la
même exécution. Aucun identifiant n'est inventé : chacun vient d'une création
réelle contre l'émulateur.

La sonde prouve le **protocole, pas le comportement**. Un inventaire vide bien
formé passe. Les curseurs et les filtres ne sont pas exercés. C'est pourquoi les
deux chiffres sont rapportés séparément et **jamais additionnés** : une route que
la sonde a atteinte reste sur la liste tant qu'un client ne l'a pas pilotée.

Le trafic synthétique est écarté partout où un chiffre s'adresse à un
utilisateur : il n'alimente ni le rapport des champs non lus, ni le verdict
d'omission, ni le score visible d'un client. La règle tient en une phrase, *le
trafic synthétique ne déplace aucun chiffre visible d'un client*, et elle est
appliquée, pas seulement énoncée.

## Maillon 4 : les enregistrements, seule source qui regarde dans l'autre sens

`feint proxy` enregistre un vrai client contre un vrai cloud ; `feint shapes`
distille ces échanges en un arbre de champs par opération, versionné sous
`shapes/`, sans aucune valeur, seulement des noms et des types.

C'est le **seul** contrôle du dépôt qui regarde dans la direction de l'omission,
et #88 en a fait un gate. Un champ déclaré fait échouer une exécution quand
**les deux** sources le cautionnent, à savoir le document le déclare et un
enregistrement le porte, et qu'aucune réponse de l'exécution ne l'a jamais porté
alors que son conteneur était servi.

Les deux moitiés sont nécessaires, et chacune seule a été mesurée fausse :

- le document seul **sur-déclare** : sur 106 champs déclarés mais absents qu'un
  enregistrement pouvait arbitrer, 83 étaient absents de la réponse du vrai
  cloud aussi ;
- un enregistrement seul **sous-déclare** : il ne couvre que ce que quelqu'un a
  enregistré.

Ce qu'aucune source ne peut arbitrer est **publié plutôt que sanctionné**, sous
`fields.unconfirmed`. Chaque entrée est à un `feint shapes --record` de devenir
soit un constat, soit rien.

Le verdict porte sur une **exécution entière**, et une exécution le déclare :
`FEINT_FIELD_GATE=1` est posé par `mise run conformance` et par la jambe
`fields` du workflow de conformance, qui pilotent tous les clients contre un
seul émulateur. Partout ailleurs les constats s'impriment et ne jugent rien,
parce qu'une jambe qui n'exerce jamais une fonctionnalité ne sert légitimement
jamais les champs que cette fonctionnalité produit. Une exécution entière non
déclarée compte comme partielle.

## Maillon 5 : le registre de preuves, sept axes, jamais additionnés

`coverage/evidence.json` est ce à quoi tout ce qui précède aboutit, opération par
opération. Sept axes, chacun répondant à une question différente, chacun voulant
dire exactement ce que son nom dit et rien de plus :

| axe | ce qu'il dit | ce qu'il ne dit pas |
|---|---|---|
| `driven` | un vrai client a atteint cette opération | que la suite ait affirmé quoi que ce soit dessus |
| `probed` | `response`, `refusal` ou `none` : ce que la sonde a **validé** | que le comportement, les curseurs ou les filtres marchent |
| `contract` | `clean`, `violating` ou `unchecked` | qu'une opération non contrôlée aille bien |
| `dataplane` | elle a été pilotée avec un runtime de machines configuré | qu'une assertion au niveau machine l'ait nommée |
| `shape` | une réponse enregistrée d'un vrai cloud la couvre | que chaque champ ait été comparé |
| `behaviour` | le cycle de vie complet d'une ressource a été observé dans une portée d'assertion déclarée | que l'effet propre de cette opération ait été affirmé |
| `negative` | elle a réellement répondu 4xx là où une suite exigeait un refus | que chaque refus qu'elle doit ait été reproduit |

**Ils ne sont jamais additionnés.** Un chiffre unique laisserait un axe faible se
faire porter par un axe fort, ce qui est la version arithmétique de la
surestimation que ce projet existe pour éviter. `contract: unchecked` n'est pas
une réussite, et `probed: refusal` ne dit pas qu'une forme de succès ait jamais
été vue.

Le registre est régénéré par `mise run evidence:update` depuis **deux passes
fraîches**, machines éteintes avec la sonde, puis machines allumées, jointes en
prenant la réponse la plus forte sur chaque axe. Cette jointure n'est sûre *que*
parce que les deux passes sont fraîches, et un rétrécissement est refusé : un
artefact qui perd un runtime échoue au lieu de publier discrètement une vérité
plus petite.

## Maillon 6 : la surface publiée, et sa version

`/_feint/health`, `/_feint/routes`, `/_feint/conformance` et `/_feint/trace` sont
ce qu'un pipeline lit. Depuis #132, chacun a une fixture versionnée sous
`internal/cli/testdata/frozen/`, l'arbre des champs et jamais une valeur, et deux
tests les gardent :

- une forme qui bouge sans que la fixture bouge échoue ;
- une fixture qui bouge sans que le `schema_version` déclaré bouge échoue.

L'historique est en ajout seul. Changer une surface gelée à dessein tient en
quatre étapes dans un seul commit, écrites dans
[RELEASING.fr.md](../RELEASING.fr.md).

Ce qui n'est **délibérément pas** gelé : la prose autour des verbes, les valeurs
derrière chaque clé, et les champs que seuls certains échanges portent. Un gel
qui attraperait cela rougirait sur des exécutions ordinaires et serait désarmé
dans la semaine.

## Où tourne chaque gate

| gate | pre-commit | pull request | nuit | release |
|---|:-:|:-:|:-:|:-:|
| `gofmt`, `vet`, `golangci-lint`, `go test -race` | ✔ | ✔ | | ✔ |
| chaque route déclare son opération upstream | ✔ | ✔ | | ✔ |
| les sections générées correspondent à leurs artefacts | ✔ | ✔ | | ✔ |
| les routes correspondent à la description d'API | ✔ | ✔ | | ✔ |
| dérive contre la baseline (code 2) | | ✔ | ✔ | ✔ |
| les vrais clients, un par un | | ✔ | ✔ | demandé à la CI |
| le gate d'omission (exécution entière) | | ✔ (jambe `fields`) | ✔ | demandé à la CI |
| le runtime de machines, dans les deux modes | | | ✔ | |
| chaque falsification déclarée mord encore | | | ✔ | |
| les surfaces gelées et leurs versions | ✔ | ✔ | | ✔ |

La preuve adossée au runtime n'est délibérément pas sur le chemin des pull
requests. Un gate qui rougit selon la météo du runner finit désarmé, ce qui est
pire que de ne pas tourner ; sa promotion se gagne par la mesure, à savoir
quatorze exécutions nocturnes vertes consécutives, comptées par un job plutôt que
par la mémoire de quelqu'un (#125).

## Les règles auxquelles tout obéit

Quatre phrases gouvernent chaque maillon ci-dessus. Chacune a été payée.

- **Un commentaire n'est pas un contrôle.** Quand un défaut est corrigé, le
  commentaire cite le test qui échoue sans le correctif. Trois audits ont
  prouvé, indépendamment, qu'un correctif rédigé en prose et jamais affirmé
  survit des mois parce qu'il se lit comme une preuve. `/falsify` en est la forme
  exécutable : retirer la garde dans une copie hors du dépôt, et exiger que le
  test nommé rougisse. La mutation doit compiler, sans quoi tous les tests
  échouent et cela ressemble exactement à une réussite. Ces mutations sont
  déclarées et rejouées, c'est la section ci-dessous.
- **Généré n'est pas dérivé.** Un bloc sous un marqueur « ne pas modifier à la
  main » dont le contenu est une constante un fichier plus loin est une
  affirmation écrite à la main déguisée en générateur. C'est arrivé deux fois.
- **Une propriété non déclarée vaut absente.** Une capacité de pilote que
  personne ne déclare est manquante, donc un contrôle se saute au lieu
  d'affirmer ce que personne n'a promis. Une exécution qui ne déclare pas
  qu'elle était entière compte comme partielle.
- **Sous-estimer une preuve coûte autant que la surestimer.** Un audit externe a
  recommandé de retirer une suite Terraform du README parce qu'un tableau ne la
  créditait pas. Cette suite appliquait vingt et une ressources et passait.

## Les gardes, rejouées

Tout ce qui précède est tenu par des tests, et un test peut cesser de mordre sans
que personne s'en aperçoive. Ce n'est pas une hypothèse ici : le CHANGELOG de
0.8.0 affirme que *neutraliser n'importe lequel des trois verrous fait rougir le
barrage dès la première tentative*, et trente exécutions consécutives sont
ensuite restées vertes avec un verrou retiré. Une falsification prouve qu'un test
mord **le jour où on la lance**, et chaque falsification du train 0.9 a été lancée
une fois, à la main, dans un script supprimé avec sa branche.

Les mutations sont donc déclarées à côté de la garde qu'elles neutralisent, dans
`tools/falsify/specs/*.json` (le fichier, la modification exacte, et le test qui
doit rougir), et rejouées chaque nuit :

```bash
mise run falsify:all           # toutes les falsifications déclarées
mise run falsify -- tools/falsify/specs/mispointed.json   # une seule
mise run falsify:selftest      # le harnais contre sa propre histoire
```

Chaque mutation s'applique dans une copie hors de l'arbre de travail, une à la
fois, et quatre verdicts sont distingués plutôt que confondus :

| verdict | ce que cela veut dire |
|---|---|
| le test a mordu | la garde est mesurée, aujourd'hui |
| **le test est resté vert** | la garde n'est pas mesurée, et c'est le test qu'il faut corriger |
| **n'a pas compilé** | verdict nul, pas favorable : tous les tests échouent, ce qui se lit comme un succès |
| **ne s'applique pas** | le code a bougé sous la déclaration |

Ce sont les deux derniers qui justifient de lancer tout cela. Avertir n'a pas
suffi (l'erreur de compilation a annulé trois verdicts en une journée, dans trois
issues sans rapport), donc le harnais refuse une mutation qui perd un nom que
l'expression d'origine utilisait, et exige la forme neutralisante (`… && false`,
`(… || true)`).

Le premier rejeu complet a payé son coût immédiatement, et pas en trouvant une
pourriture dans l'émulateur. Sur huit specs, quatre ne tenaient plus : trois
étaient écrites dans le style suppressif que la règle refuse, une nommait deux
gardes que la forme finale de #179 avait retirées, et une déclarait un test qui
ne correspondait pas à la garde placée dessous, deux conditions `synthetic`
distantes d'un écran dans le même fichier, l'une relevant de #88 et l'autre de
#163. Ce dernier cas est celui pour lequel le rejeu existe : la mutation a
compilé, et le test nommé est resté vert.

## Ce qui n'est pas encore fermé

Un maillon de la chaîne ci-dessus est énoncé sans être appliqué. C'est une issue
ouverte, et elle est nommée ici parce qu'une page qui ne décrirait que la moitié
terminée serait le défaut que ce projet supprime.

- **Rien ne mesure ce qu'une release fait à un consommateur**
  ([#170](https://github.com/stephrobert/feint/issues/170)). `schema_version` est
  le signal qui permet à un pipeline de s'apercevoir d'une cassure ; rien ne
  vérifie qu'un pipeline **puisse** s'en apercevoir. `probed` est passé de
  booléen à chaîne, et un consommateur qui le lit comme vrai compte chaque refus
  comme un succès.

Tant qu'elle n'est pas fermée, l'énoncé honnête est celui par lequel cette page
commence : la chaîne est mesurée, maillon par maillon, et ce maillon-là est
mesuré par de la prose.

Les deux autres que cette section listait sont fermées et repliées dans la page
ci-dessus. [#171](https://github.com/stephrobert/feint/issues/171) a donné au
registre de preuves une provenance que la jointure compare, de sorte que
supprimer une assertion de conformance rétrograde ce qu'elle prouvait au lieu de
rester une phrase écrite deux fois ;
[#169](https://github.com/stephrobert/feint/issues/169) est le rejeu décrit une
section plus haut.

## Lire les chiffres soi-même

```bash
feint serve --contracts contracts &
curl -s localhost:4599/_feint/conformance | jq '.evidence["instance/v1/API.ListServers"]'
curl -s localhost:4599/_feint/health | jq '{schema_version, capabilities}'
```

Les tableaux générés du [README](../README.fr.md) et de
[docs/routes.md](routes.md) sont rendus depuis les mêmes artefacts, par
`mise run docs:coverage`, et `feint docs --check` sort en 2 quand une page et son
artefact ne s'accordent pas.
