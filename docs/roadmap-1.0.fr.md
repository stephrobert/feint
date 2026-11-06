# Feint 1.0 — Le laboratoire cloud local

Ce document fixe le plan d'exécution de la prochaine version majeure. L'historique
long reste dans [roadmap.fr.md](roadmap.fr.md) ; l'issue qui porte ce découpage
est [#847](https://github.com/stephrobert/feint/issues/847).

## La promesse de la version

> **Lancer. Casser. Réinitialiser. Prouver.**

Feint 1.0 n'est pas conditionné par un nombre de routes. C'est la version où un
utilisateur peut prendre un vrai workflow Scaleway, OUTSCALE ou Exoscale et
utiliser Feint comme laboratoire cloud local, pas seulement comme fixture d'API.

Le workflow de référence est :

```text
vrai Terraform/OpenTofu ou CLI officiel
                 │
                 ▼
              feint run
                 │
       ┌─────────┼─────────┐
       │         │         │
 control plane   │   fautes déterministes
       │         │
       ▼         ▼
 topologie désirée
       │
       ▼
 réconciliation runtime
       │
       ├── Incus / OVN
       └── Object Storage local
       │
       ▼
 observation normalisée
       │
       ├── debugger de topologie
       ├── explain/evidence
       └── checkpoint/reset
```

## Pourquoi ce découpage

Les demandes des utilisateurs d'autres émulateurs se concentrent sur les
fonctions qui raccourcissent la boucle de développement : ressources locales
réelles, persistance, reset, hot reload, intégration directe à l'outil et fautes
reproductibles.

Références :
- LocalStack EC2/runtime local :
  https://github.com/localstack/localstack/issues/691
- LocalStack persistance :
  https://github.com/localstack/localstack/issues/6281
- Floci hot reload :
  https://github.com/floci-io/floci/issues/553
- Floci reset/nuke :
  https://github.com/floci-io/floci/issues/803
- Floci fault injection déterministe :
  https://github.com/floci-io/floci/issues/4623

Feint possède déjà les briques rares qui permettent d'en faire un produit
cohérent : vrais clients providers, dialectes d'erreur mesurés, snapshots,
ownership runtime, témoins Incus/OVN et Runtime Contract v1.

## Chantiers

1. **#848 — `feint run`** : une commande possède tout le cycle du lab.
2. **#849 — interception locale bornée** : aucun run annoncé local ne sort
   silencieusement vers le cloud réel.
3. **#850 — Object Storage** : un dataplane S3 local commun derrière les portes
   spécifiques aux providers.
4. **#851 — campagnes de fautes rejouables** : schedules déterministes, seeds et
   fautes de transport.
5. **#852 — debugger de topologie** : modèle machine-readable d'abord, vue UI
   ensuite.
6. **#853 — checkpoint/reset** : restaurer le store, le runtime et les octets,
   puis retirer les ressources Feint en trop.
7. **#193 et #487** : drift volontaire et explication de la preuve existante.
   #490 reste différé jusqu'à ce que les primitives aient un usage réel.

## Ordre de dépendance

```text
Runtime Contract v1 / observation normalisée
                 │
        ┌────────┴────────┐
        │                 │
      #849              #852
 interception          topologie
        │
      #850
 object storage
        │
        └────────┐
                 │
#193 ──┐       #853
mutate │       reset/checkpoint
       │          │
#851 ──┼──────────┼──> #848 feint run
faults │          │
       └──────────┘
                 │
               #854
          acceptance 1.0
```

## Ce que la 1.0 n'achète pas

- Kapsule/SKS ;
- tout S3 ;
- une DSL de scénario ;
- un cloud privé généraliste ;
- de la télémétrie cachée ;
- une modification globale DNS/CA comme chemin nominal ;
- des promesses déduites de tests unitaires ou d'un HTTP 200.

## Gate de sortie — #854

La version ne sort que si, depuis les artefacts publiés sur un Linux supporté,
une personne extérieure au développement peut :

1. lancer une stack publique nommée sans compte cloud ni vrai secret ;
2. exercer le runtime réel là où le mode le promet ;
3. garder l'Object Storage local pour le workflow de référence ;
4. expliquer une connectivité autorisée et une connectivité refusée ;
5. poser un checkpoint ;
6. injecter une faute déterministe et observer le comportement du vrai client ;
7. créer un drift hors Terraform et le faire détecter ;
8. revenir au checkpoint avec le même état observable normalisé ;
9. terminer sans laisser de ressource runtime appartenant à Feint.

Chaque pilier possède une falsification qui prouve que son test sait devenir
rouge.

Avant le tag, le registre downstream doit contenir au moins cinq configurations
publiques choisies indépendamment et exécutées par quelqu'un d'autre que le
mainteneur, ou cinq dépôts externes avec une intégration Feint reproductible.
L'objectif plus long de dix configurations dans
[adoption.md](adoption.md) reste inchangé.
