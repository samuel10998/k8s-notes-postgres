# Postup

Čo som robil krok po kroku, s príkazmi, výsledkami a problémami. Prostredie: Windows 11, PowerShell 5.1, Docker Desktop. Výstupy z terminálu som nahrával cez `Start-Transcript` do `logs/`, ktorý je v `.gitignore`.

## Krok 1: repo a aplikácia (`main`)

**Cieľ:** mať repo na GitHube a v ňom aplikáciu presne tak, ako ju dodali vývojári.

1. Na GitHube som vytvoril verejné repo `k8s-notes-postgres` s README a licenciou MIT.
2. Lokálne som repo inicializoval a prepojil s GitHubom:

   ```powershell
   git init
   git remote add origin https://github.com/samuel10998/k8s-notes-postgres.git
   git fetch origin
   git reset --mixed origin/main
   git branch -M main
   git branch --set-upstream-to=origin/main main
   ```

   Repo som stiahol ako zip, preto tento postup. Jednoduchšie je `git clone`, ktorý spraví všetky tieto kroky naraz.

3. Do repa som pridal priečinok `app/` bez mojich zmien.
4. Pridal som `.gitignore` s `logs/`, aby sa výstupy z `Start-Transcript` nedostali do Gitu.
5. Commit a push do `main`.

### Problém: logy v staging area

`.gitignore` som vytvoril až po prvom `git add .`, takže `logs/logs_1.txt` už bol pridaný. `.gitignore` sa týka len súborov, ktoré Git ešte nesleduje, preto ďalšie `git add .` súbor neodstránili.

Riešenie:

```powershell
git rm -rf --cached logs/
```

`-f` bolo potrebné, lebo transcript do súboru stále zapisoval a verzia v staging area sa líšila od súboru na disku. Do commitu išlo 5 súborov bez logov.

**Poučenie:** `.gitignore` vytvoriť pred prvým `git add`.

## Krok 2: Dockerfile (`feature/docker`)

**Cieľ:** malý image API, ktorý sa postaví vždy rovnako a nebeží ako root.

```powershell
git switch -c feature/docker
```

### Výber base image

- Go je kompilovaný jazyk. Na build treba kompilátor (image `golang`), na beh stačí hotová binárka.
- Preto multi-stage build: build stage `golang:1.27.4-alpine3.24`, výsledný image `alpine:3.24.3`.
- Go 1.27 je aktuálna podporovaná verzia. `go.mod` vyžaduje 1.24 alebo novšiu.
- Alpine som zvolil, lebo je malý a má shell, takže sa dá ísť do kontajnera (`docker exec`, neskôr `kubectl exec`). Distroless alebo scratch by boli menšie, ale bez shellu.
- Tagy sú presné (verzia Go aj Alpine), nie `latest`.

### Čo robí Dockerfile

1. Build stage: `WORKDIR /app`, skopíruje `go.mod` a `go.sum`, `go mod download`, skopíruje zvyšok kódu, `go build` so `CGO_ENABLED=0` a verziou z `ARG VERSION`.
2. Výsledný stage: vytvorí používateľa `appuser` s UID 10001, skopíruje binárku z build stage, `USER 10001`, `EXPOSE 8080`, `CMD ["/app/notes-api"]`.

### Chyby pri písaní Dockerfile

| Chyba | Čo by spôsobila |
|---|---|
| `WORKDIR app` (relatívna cesta) | pripojí sa k `/go` z golang image, výsledok `/go/app` |
| `COPY go.mod go.sum` bez cieľa | posledný argument je cieľ, `go.mod` by sa skopíroval do súboru `go.sum` |
| chýbal `go mod download` | závislosti by sa sťahovali pri každej zmene kódu |
| chýbalo kopírovanie `main.go` | `go build` by skončil chybou `no Go files` |
| príkaz `go build` bez `RUN` | `unknown instruction` |
| verzia `1.0.0` natvrdo | pre novú verziu by sa musel meniť Dockerfile, preto `ARG VERSION` |
| `./notes-api` v build stage | appka by sa spúšťala počas buildu, spustenie patrí do `CMD` |

### Build a veľkosť

```powershell
docker build --build-arg VERSION=1.0.0 -t notes-api:1.0.0 app
docker images notes-api
```

```
IMAGE             ID             DISK USAGE   CONTENT SIZE
notes-api:1.0.0   620719b4f41e       30.6MB         10.2MB
```

- `CONTENT SIZE` je skomprimovaný image (prenos pri `push`, `pull`, `kind load`)
- `DISK USAGE` je miesto na disku: skomprimované vrstvy a k nim rozbalená kópia
- `docker images golang` nevypísal nič. BuildKit si base image pri builde drží vo vlastnej cache, nie medzi bežnými image. Zobrazil by sa až po `docker pull`.

### Test 1: bez hesla k databáze

```powershell
docker run -d --name notes-nopass notes-api:1.0.0
docker ps -a --filter name=notes-nopass
docker logs notes-nopass
```

```
STATUS: Exited (1)
level=ERROR msg="chýba premenná prostredia DB_PASSWORD, končím"
```

Appka bez povinnej premennej hneď skončí s kódom 1 a v logu povie prečo. V Kubernetes by to bol `CrashLoopBackOff`.

### Test 2: s heslom, bez databázy

```powershell
docker run -d --name notes-nopass-v2 -p 8081:8080 -e DB_PASSWORD=heslo notes-api:1.0.0
curl.exe -i http://localhost:8081/healthz
curl.exe -i http://localhost:8081/readyz
curl.exe http://localhost:8081/api/info
```

```
HTTP/1.1 200 OK
{"status":"ok"}

HTTP/1.1 503 Service Unavailable
{"error":"dial tcp [::1]:5432: connect: connection refused","status":"not ready"}

{"app":"notes-api","env":"local","hostname":"030e84253822","version":"1.0.0"}
```

- `/healthz` 200: proces žije, databázu nekontroluje
- `/readyz` 503: appka použila predvolený `DB_HOST=localhost`. Vnútri kontajnera je `localhost` samotný kontajner (`[::1]` je localhost v IPv6) a databáza v ňom nebeží. V Kubernetes bude `DB_HOST` meno Service databázy.
- `/api/info`: verzia `1.0.0` z `--build-arg`, `hostname` je začiatok ID kontajnera

### Test 3: používateľ

```powershell
docker exec notes-nopass-v2 id
```

```
uid=10001(appuser) gid=10001(appuser) groups=10001(appuser)
```

### Test 4: logy

```powershell
docker logs notes-nopass-v2
```

- prvý riadok vypíše nastavenia, s ktorými appka štartuje (`db_host=localhost`, port, verzia)
- každých 5 sekúnd: `databáza zatiaľ nie je dostupná, skúsim znova o 5 s`
- požiadavky na `/api/info` sú v logu, `/healthz` a `/readyz` sa zámerne nelogujú
- `remote=172.17.0.1` je brána siete Docker, požiadavky z PC idú cez mapovanie portu

### Test 5: druhá verzia a cache

```powershell
docker build --build-arg VERSION=1.1.0 -t notes-api:1.1.0 app
```

```
CACHED [build 2/6] WORKDIR /app
CACHED [build 3/6] COPY go.mod go.sum ./
CACHED [build 4/6] RUN go mod download
CACHED [build 5/6] COPY . .
[build 6/6] RUN CGO_ENABLED=0 go build -ldflags "-X main.version=1.1.0" -o notes-api .   7.6s
```

Zmena `VERSION` nespôsobila nové sťahovanie závislostí, znova prebehol len `go build`. Celý build trval 9,5 s.

```powershell
docker run -d --name notes-nopass-v3 -p 8082:8080 -e DB_PASSWORD=heslo notes-api:1.1.0
curl.exe http://localhost:8082/api/info
curl.exe http://localhost:8081/api/info
```

```
{"app":"notes-api","env":"local","hostname":"f982eb3bf236","version":"1.1.0"}
{"app":"notes-api","env":"local","hostname":"030e84253822","version":"1.0.0"}
```

Obe verzie bežali naraz, každá vo vlastnom kontajneri a na inom porte PC.

### Upratanie

```powershell
docker stop notes-nopass-v2 notes-nopass-v3
docker rm notes-nopass notes-nopass-v2 notes-nopass-v3
```

Bežiaci kontajner sa nedá zmazať cez `docker rm`, najprv `docker stop`. `docker stop` pošle appke `SIGTERM` a appka sa korektne ukončí. Image `1.0.0` a `1.1.0` zostali pre klaster.

### Git

```powershell
git add .
git commit -m "added Multistage Dockerfile go + alpine"
git push -u origin feature/docker
```

Pri prvom pushi novej vetvy treba `-u origin <vetva>`, potom stačí `git push`. Potom pull request do `main`, merge, `git switch main` a `git pull`.
