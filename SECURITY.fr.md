# Politique de sécurité

**Autre langue :** [English](./SECURITY.md)

## Ce qu'est Feint, et ce que cela implique pour vous

Feint émule des API de cloud pour que vous puissiez lancer vos tests sans compte.
C'est un **outil de développement**. Il accepte tous les credentials qu'on lui
donne sans en vérifier aucun, il répond en HTTP en clair, et il garde son état en
mémoire. C'est délibéré — tout l'intérêt est de tourner sans compte — et cela rend
l'émulateur impropre à autre chose qu'un poste de travail ou un runner de CI.

**Ne l'exposez pas à un réseau que vous ne maîtrisez pas.** Liez-le à une adresse
de boucle locale, ce que `feint serve` fait par défaut, ou à un réseau dont le
conteneur qui l'héberge ne peut pas sortir.

Écouter sur la boucle locale ne suffit pas à soi seul, et l'émulateur ne prétend
pas le contraire. Une page web que l'opérateur visite peut résoudre son propre nom
vers `127.0.0.1` et faire émettre au navigateur des requêtes ici, pour son compte
— c'est le *DNS rebinding*, contre lequel une adresse d'écoute ne protège de rien.
Donc, lorsque Feint est lié à une adresse de boucle locale, il **refuse les
requêtes dont l'en-tête `Host` en nomme une autre**, ce qu'un navigateur ne peut
pas falsifier. Liez-le ailleurs avec `--addr` et le contrôle s'efface, puisque
cette exposition a été demandée.

Deux conséquences qui méritent d'être dites franchement :

- **N'importe quel credential est accepté.** La signature v4, `X-Auth-Token` et
  `EXO2-HMAC-SHA256` sont analysés et jamais vérifiés. Tout ce qui peut atteindre
  le port peut lire et supprimer tout ce que l'émulateur détient.
- **Avec `--vm`, l'émulateur démarre de vraies machines.** Il crée des conteneurs
  ou des machines virtuelles, des ponts et des règles de pare-feu sur l'hôte, via
  le CLI Incus et avec vos privilèges. Tout ce qu'il crée est étiqueté et
  `feint clean` retire exactement cela ; rien d'autre n'est touché. Mais un
  émulateur joignable depuis l'extérieur est, dans ce mode, un moyen de lancer
  des conteneurs sur votre hôte.

## Signaler une vulnérabilité

Signalez en privé via **Security → Report a vulnerability** sur ce dépôt. Cela
ouvre un avis privé que seuls les mainteneurs peuvent lire.

Merci de ne pas ouvrir d'issue publique pour quoi que ce soit d'exploitable.

Incluez ce que vous aimeriez recevoir : ce que vous avez fait, ce qui s'est passé,
ce que vous attendiez, et le plus petit moyen de le reproduire. Une preuve de
concept est bienvenue et jamais exigée.

Vous recevrez un accusé de réception sous une semaine. Si un signalement conduit à
un correctif, l'avis vous nomme, sauf préférence contraire de votre part.

## Ce qui compte

Dans le périmètre, et pris au sérieux :

- Tout ce qui permet à une requête de s'échapper de l'émulateur : injection de
  commande dans le CLI du runtime, traversée de chemin par un identifiant de
  ressource, une route qui touche une ressource de l'hôte que l'émulateur n'a pas
  créée.
- Tout ce qui fait que `feint clean` retire quelque chose qu'il n'a pas créé. Le
  balayage est délimité par étiquette précisément pour que ce soit impossible, et
  un contournement est une vraie trouvaille.
- Un plantage atteignable depuis une requête. L'émulateur est une dépendance de
  test : une panique emporte la suite de tests de quelqu'un avec elle.
- Une compromission d'une dépendance ou de la chaîne de construction. Le module
  n'a aucune dépendance Go externe, ce qui est une réduction délibérée de cette
  surface.

Hors périmètre, parce que c'est la conception documentée :

- Les credentials ne sont pas vérifiés.
- Le point d'entrée par défaut parle HTTP plutôt que HTTPS.
- Les faux credentials versionnés sous
  `tools/conformance/*/fake-credentials.env` sont publics à dessein. Ils n'ouvrent
  rien ; ils existent parce que les clients officiels refusent de signer une
  requête dont les credentials ne sont pas bien formés.

## Versions supportées

Le projet est en pré-1.0. Les correctifs arrivent sur la branche par défaut et
dans la version suivante ; il n'y a pas de rétroportage vers des tags antérieurs.
