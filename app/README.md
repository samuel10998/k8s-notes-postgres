# notes-api

Malé REST API na poznámky. Poznámky ukladá do PostgreSQL.

## Technológie

| | |
|---|---|
| jazyk | Go 1.24 alebo novší (`go.mod` hovorí `go 1.24`, funguje aj s novšími verziami) |
| knižnice | jediná závislosť je `github.com/lib/pq` (ovládač PostgreSQL, čisté Go) |
| databáza | PostgreSQL 14 alebo novší (vyvíjané a testované na 16) |

## Build a spustenie

Závislosti sú v `go.mod` a `go.sum`.

```sh
go mod download
CGO_ENABLED=0 go build -ldflags "-X main.version=1.0.0" -o notes-api .
./notes-api
```

- výsledok je jeden spustiteľný súbor `notes-api` bez ďalších závislostí, na beh nepotrebuje Go ani zdrojové súbory
- `CGO_ENABLED=0` = binárka nezávisí od C knižníc systému
- `-X main.version=1.0.0` nastaví verziu, ktorú vracia `/api/info`. Bez toho je verzia `dev`.
- appka počúva na porte **8080** (dá sa zmeniť cez `PORT`)

## Premenné prostredia

| Premenná | Predvolená hodnota | Popis |
|---|---|---|
| `PORT` | `8080` | port HTTP servera |
| `APP_ENV` | `local` | názov prostredia, len sa zobrazí v `/api/info` |
| `DB_HOST` | `localhost` | adresa PostgreSQL servera |
| `DB_PORT` | `5432` | port PostgreSQL |
| `DB_NAME` | `notes` | meno databázy |
| `DB_USER` | `notes` | používateľ databázy |
| `DB_PASSWORD` | – | **povinná**, heslo používateľa. Bez nej appka hneď skončí. |
| `DB_SSLMODE` | `disable` | SSL k databáze (`disable`, `require`…) |

Databáza a používateľ musia existovať. Appka si vytvorí len tabuľku `notes`.

## Endpointy

| Metóda a cesta | Na čo je | Odpoveď |
|---|---|---|
| `GET /healthz` | appka žije | vždy `200` `{"status":"ok"}`, databázu nekontroluje |
| `GET /readyz` | appka vie obsluhovať požiadavky | `200` keď je databáza dostupná, inak `503` aj s chybou |
| `GET /api/info` | info o behu | `200` JSON: `app`, `version`, `env`, `hostname` |
| `GET /api/notes` | zoznam poznámok | `200` JSON pole, najnovšie prvé, najviac 100 |
| `POST /api/notes` | nová poznámka | telo `{"text":"..."}` (1 až 500 znakov), `201` s uloženou poznámkou |

Pri chybe databázy vracajú `/api/notes` `503`, pri zlom tele `POST` vracia `400`.

Každá poznámka má `id`, `text`, `created_at` a `host`, teda hostname kontajnera, ktorý ju uložil.

### Príklady (Windows PowerShell 5.1)

```powershell
curl.exe -i http://localhost:8080/healthz
curl.exe -i http://localhost:8080/readyz
curl.exe http://localhost:8080/api/info
curl.exe -X POST http://localhost:8080/api/notes -H "Content-Type: application/json" -d '{\"text\":\"prva poznamka\"}'
curl.exe http://localhost:8080/api/notes
```

V PowerShelli 5.1 treba úvodzovky vnútri JSON písať ako `\"`. V PowerShelli 7.3 a novšom a v Linuxe sa píšu bez spätnej lomky: `-d '{"text":"prva poznamka"}'`. V príkladoch je text bez diakritiky, lebo PowerShell 5.1 ju pri posielaní cez `curl.exe` môže pokaziť.

## Správanie

- **štart:** HTTP server naštartuje hneď, aj keď databáza ešte nie je dostupná. Pripojenie skúša každých 5 sekúnd a pri prvom úspechu vytvorí tabuľku `notes`, ak ešte neexistuje. Viac inštancií naraz si pri vytváraní tabuľky neprekáža.
- **chýbajúce `DB_PASSWORD`:** appka zapíše chybu do logu a skončí s kódom 1.
- **výpadok databázy:** appka beží ďalej, `/readyz` vracia `503` a `/api/notes` `503`. Keď sa databáza vráti, appka sa pripojí sama.
- **logy:** všetko ide na stdout, jeden riadok na udalosť vo formáte `kľúč=hodnota`. Loguje sa štart, zmena stavu databázy, chyby a každá požiadavka na `/api/*`. `/healthz` a `/readyz` sa nelogujú, aby nezahltili log.
- **ukončenie:** po `SIGTERM` dokončí rozbehnuté požiadavky (najviac 10 sekúnd) a skončí s kódom 0.
- **disk:** appka nič nezapisuje na disk, všetky dáta sú v databáze.

## Čo tu nie je

Dockerfile, Docker Compose a Kubernetes manifesty. Tie pripravuje DevOps.
