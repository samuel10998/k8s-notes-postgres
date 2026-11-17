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

---

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

---

## Krok 3: lokálny klaster (`feature/cluster`)

**Cieľ:** klaster, ktorý si ktokoľvek vytvorí z repa jedným príkazom, s presnou verziou Kubernetes, a v ňom image `notes-api` bez registra.

### Prečo kind a nie Kubernetes z Docker Desktop

Kind (Kubernetes in Docker) spúšťa každý uzol klastra ako Docker kontajner. Používa sa na lokálny vývoj, testovanie a v CI pipeline. V produkcii by bol spravovaný klaster (AKS, EKS, GKE) alebo OpenShift.

| | Docker Desktop Kubernetes | kind z config súboru |
|---|---|---|
| vytvorenie | klik v nastaveniach | príkaz a súbor v repe |
| zopakuje ho niekto iný | nie | áno, z rovnakého súboru |
| verzia Kubernetes | podľa Docker Desktop | presne určená |
| počet uzlov | 1 | koľko zadám |
| zmazanie a nový klaster | zložité | jeden príkaz |

### Môj klaster vs. predvolený `kind create cluster`

| | Predvolený (bez configu) | Môj (`cluster/kind-config.yaml`) |
|---|---|---|
| meno, kontext | `kind`, `kind-kind` | `notes`, `kind-notes` |
| uzly | 1 uzol, ktorý je control-plane aj worker | 1 control-plane + 2 workery |
| verzia | predvolená pre danú verziu kind (pri kind 0.33.0 je to 1.37) | `v1.35.8`, presne určená |
| kde bežia Pody | na jedinom uzle | na workeroch, control-plane má taint a appky na ňom nebežia |

S tromi uzlami vidno, ako scheduler rozdeľuje Pody na rôzne uzly, a dá sa vyskúšať výpadok uzla.

### Verzie

```powershell
winget install Kubernetes.kind
kind version
kubectl version --client
```

```
kind v0.33.0 go1.26.7 windows/amd64
Client Version: v1.34.1
```

**Prečo presná verzia Kubernetes:**
- `kubectl` je podporovaný len do rozdielu 1 minor verzie od klastra. Pri kubectl 1.34 to znamená klaster 1.33 až 1.35. Predvolená 1.37 je príliš ďaleko a `kubectl` na to upozorní, niektoré príkazy sa môžu správať inak.
- Bez určenej verzie by klaster dostal tú, ktorú má práve nainštalovaná verzia kind. Po aktualizácii kind by bol iný, a teda nie opakovateľný.
- V praxi sa lokálny klaster nastavuje na rovnakú verziu ako produkcia.

**Prečo musí byť image pri každom uzle:** uzol bez `image:` dostane predvolenú verziu kind (1.37). Control-plane by mal 1.35 a workery 1.37. Kubelet na workeri nesmie byť novší ako API server na control-plane, takže taký klaster nie je podporovaný a môže sa správať nepredvídateľne.

**Ako som vybral image:**
1. zistil som verziu kind (`0.33.0`) a kubectl (`1.34.1`)
2. v release notes tejto verzie kind na GitHube (`kubernetes-sigs/kind`, Releases) som v časti „Images pre-built for this release“ vybral image s verziou o najviac 1 minor od kubectl
3. skopíroval som ho celý aj s digestom: `kindest/node:v1.35.8@sha256:07b2536e...`

Node image patrí ku konkrétnej verzii kind. Image z inej verzie nemusí s mojím kind fungovať, napríklad pri `kind load`. Digest zaručí, že dostanem presne ten image, nie iný s rovnakým tagom. Kind 0.33.0 a tento image som mal aj v prvom projekte.

### Config

`cluster/kind-config.yaml`:
- `kind: Cluster`, `apiVersion: kind.x-k8s.io/v1alpha4`. Nie je to objekt Kubernetes, číta ho len `kind create cluster`, nie `kubectl apply`.
- `name: notes`
- `nodes`: control-plane a 2 workery, každý s rovnakým `image:`

### Vytvorenie a overenie

```powershell
kind create cluster --config cluster/kind-config.yaml
```

| Krok vo výstupe | Čo sa stalo |
|---|---|
| Ensuring node image | stiahol `kindest/node:v1.35.8` |
| Preparing nodes | spustil 3 Docker kontajnery, každý je jeden uzol |
| Writing configuration | pripravil konfiguráciu pre `kubeadm` |
| Starting control-plane | API server, etcd, scheduler, controller manager |
| Installing CNI | sieť pre Pody (kindnet) |
| Installing StorageClass | predvolená StorageClass `standard` pre PVC |
| Joining worker nodes | pripojil workery ku control-plane |
| Set kubectl context | `kubectl` pracuje s `kind-notes` |

```powershell
kubectl config current-context
kubectl get nodes
docker ps
```

```
kind-notes

NAME                  STATUS   ROLES           AGE     VERSION
notes-control-plane   Ready    control-plane   3m8s    v1.35.8
notes-worker          Ready    <none>          2m55s   v1.35.8
notes-worker2         Ready    <none>          2m55s   v1.35.8
```

- `ROLES <none>` pri workeroch je normálne, workery nemajú štítok s rolou
- v `docker ps` sú 3 kontajnery `kindest/node:v1.35.8`. Pri control-plane je `127.0.0.1:9385->6443/tcp`: 6443 je port API servera a cez náhodný port 9385 na PC sa k nemu pripája `kubectl`.
- `kubectl config get-contexts` ukázal aj `kind-aspecta` z prvého projektu. Klaster už neexistuje, zostal len záznam v kubeconfigu.

### Image v klastri

Každý uzol má vlastné úložisko image (containerd vnútri kontajnera uzla), oddelené od Docker Desktop. `notes-api` nie je v žiadnom registri, takže si ho uzol nemá odkiaľ stiahnuť. Bez nahratia by Pod skončil v `ErrImagePull` alebo `ImagePullBackOff`.

```
Docker Desktop                kind load docker-image                  uzly klastra
docker images                                                       (containerd)
notes-api:1.0.0  ──►  export image do archívu  ──►  import  ──►  notes-control-plane
notes-api:1.1.0                                      import  ──►  notes-worker
                                                     import  ──►  notes-worker2
```

`kind load` tu nahrádza register. V praxi by CI pipeline image pushla do registra (Docker Hub, GHCR, ACR) a uzly by si ho stiahli samy.

```powershell
kind load docker-image notes-api:1.0.0 notes-api:1.1.0 --name notes
docker exec notes-worker crictl images | Select-String notes-api
```

```
Image: "notes-api:1.0.0" with ID "sha256:620719b4..." not yet present on node "notes-worker2", loading...
... (2 image × 3 uzly = 6 riadkov)

docker.io/library/notes-api   1.0.0   fbd2c16680c99   10.2MB
docker.io/library/notes-api   1.1.0   6ef8726983a7d   10.2MB
```

- `--name notes` je meno klastra. Bez neho by kind hľadal klaster `kind`.
- PostgreSQL sa nenahráva, je to oficiálny image a uzol si ho stiahne z Docker Hubu sám.
- `docker.io/library/` je plné meno image. Krátke `notes-api:1.0.0` v manifeste sa doplní rovnako.
- ID v `crictl` sú iné ako v `docker images`, ale je to ten istý image. Docker ukazuje odtlačok zoznamu manifestov, `crictl` odtlačok konfigurácie (oba boli vo výstupe buildu).
- Pri tagu ako `1.0.0` má Pod predvolené `imagePullPolicy: IfNotPresent` a použije image z uzla. Pri `latest` by sa stále pokúšal sťahovať z registra.
- Po zmazaní a novom vytvorení klastra, aj po každom novom builde, treba `kind load` zopakovať.

### Problémy

| Problém | Príčina | Riešenie |
|---|---|---|
| `kind` nie je rozpoznaný príkaz | kind nebol na tomto PC nainštalovaný | `winget install Kubernetes.kind` |
| po inštalácii stále nerozpoznaný | PowerShell si `PATH` načítal pred inštaláciou | zatvoriť a otvoriť nový PowerShell |
| `cannot find the path specified` pri `kind create cluster` | bol som v priečinku `logs`, relatívne cesty sa počítali od neho. Transcript išiel do `logs\logs\`. | spúšťať príkazy z koreňa repa |
| `image:` odsadené viac ako `role:` | YAML ho bral ako pokračovanie hodnoty `role` | kľúče jednej položky zoznamu musia začínať v rovnakom stĺpci |

### Git

```powershell
git switch -c feature/cluster
git add cluster/kind-config.yaml
git commit -m "added kind cluster (control-plane + 2 workers)"
git push -u origin feature/cluster
```

