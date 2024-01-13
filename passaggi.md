# Alpi Web Mail
Un semplice frontend sul server imap per leggere e mandare emails via web.
La repository di partenza molto interessante è https://github.com/migadu/alps.
Ho tolto delle parti che non mi interessano ed ho cambiato l'organizzazione dei files.
Il file di configurazione example.conf va editato e messo sotto il nome alpi.conf.
Il frontend parte in localhost:1323 secondo il conf file.
Se si usa il log file nel config, l'output del server va nel file di log.

# Problemi nel Compose
Per mandare emails, il login va effettuato con un indirizzo email valido e non solo con 
l'username.

## Settare il service
Guarda il file readme_hetzner per quanto riguarda la creazione di un sub domain. Su github
non ho messo nessuna info a riguardo, anche negli altri services.