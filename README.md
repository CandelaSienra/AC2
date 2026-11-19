# Actuación en clase - Kubernetes en AWS EKS

## Objetivo

Diagnosticar y corregir los problemas de un proyecto que despliega una aplicación en **AWS EKS** (infraestructura con Terraform, pipeline con GitHub Actions, imagen Docker y manifiestos de Kubernetes) hasta que la aplicación responda correctamente a través de la URL pública del **Application Load Balancer (ALB)**.

## Contexto

Se te entregó un repositorio con una aplicación web en Go y todo lo necesario para desplegarla en EKS con **un único pipeline**: crea la infraestructura, construye la imagen, la sube a ECR, la despliega en el cluster y valida que responda a través del ALB. Sin embargo, el proyecto tiene varios problemas que tenés que identificar y corregir haciendo commits hasta que el pipeline quede en verde y la app funcione.

```
┌──────────────────────────────────────────────────────────────────────────┐
│                       GitHub Actions — deploy.yaml                       │
│                                                                          │
│ Terraform ──► Build & Push ──► Deploy to EKS ──► Validate ──► Evidence   │
│ (infra)        (ECR)         (kubectl apply)    (curl ALB)   (alb_url)   │
└──────────────────────────────────────────────────────────────────────────┘
                      │
                ┌─────▼──────┐
                │  Amazon    │
                │    ECR     │  ← web-app:{sha}
                └─────┬──────┘
                      │
┌─────────────────────▼───────────────────────────────────────────┐
│                            AWS VPC                              │
│                                                                 │
│  Internet ──► ALB (Ingress) ──► Service ClusterIP ──► Pods      │
│               creado por el      web-app:80           web-app   │
│               AWS LB Controller                       :8080     │
│                                                                 │
│               EKS cluster + managed node group (3 subnets)      │
└─────────────────────────────────────────────────────────────────┘
```

## Pistas

Hay **12 problemas** repartidos en el repositorio. Estas pistas te orientan, pero no te dicen exactamente qué cambiar: los logs de la pestaña "Actions", `terraform validate`, `kubectl describe`, `kubectl logs` y `kubectl get events` son tus mejores aliados (ver [Comandos útiles](#comandos-útiles)).

> El primer `terraform apply` exitoso tarda ~15-20 min (el cluster EKS es lento en crearse). Los siguientes, si la infra no cambió, tardan unos pocos minutos. Leé bien cada error antes de hacer el próximo push.

### 1. Terraform (3 problemas)

- El pipeline es estricto con el formato. `terraform fmt` te dice qué archivo no cumple… y también sabe arreglarlo.
- Un módulo solo acepta los argumentos que declara en su `variables.tf`. Compará nombre por nombre.
- AWS no acepta cualquier combinación de tamaños para un node group. El error del `apply` dice cuál es el problema.

### 2. Pipeline (3 problemas)

- Un output de job que apunta a un step que no existe no da error: queda vacío. ¿Cómo se llama realmente ese step?
- Las credenciales de AWS Academy tienen tres partes. ¿Todos los jobs las usan completas?
- En bash, las comillas simples y las dobles no son lo mismo. ¿Qué recibe realmente `envsubst`? Mirá qué imagen terminan usando los pods.

### 3. Imagen Docker (1 problema)

- La imagen se construye y se publica, pero el contenedor no arranca y dice que un archivo que sí está "no existe". ¿Qué tienen de distinto la imagen donde se compila y la imagen donde se ejecuta?

### 4. Kubernetes (5 problemas)

- Un Deployment tiene que poder encontrar los pods que él mismo crea.
- Las probes solo sirven si consultan un endpoint que la app realmente expone. `app/main.go` es la referencia.
- Un Service conecta su propio puerto con el puerto donde escucha el contenedor.
- El tipo de target del ALB tiene que ser compatible con el tipo de Service. Si el Ingress no tiene dirección, `kubectl describe ingress` cuenta por qué.
- La app responde, pero algunos campos aparecen vacíos. Una ServiceAccount se identifica por su nombre **y** por su namespace.

> **Nota:** `app/main.go` no debe modificarse. Los demás archivos deben corregirse para que sean consistentes con su implementación.

## Estructura del repositorio

```
├── app/
│   ├── Dockerfile              # Build multi-stage de la imagen
│   └── main.go                 # Servidor HTTP en Go (puerto 8080) — NO MODIFICAR
├── k8s/                        # Manifiestos que aplica el pipeline
│   ├── 00-namespace.yaml
│   ├── 01-rbac.yaml            # ServiceAccount + permiso para leer su nodo
│   ├── 02-deployment.yaml      # ${IMAGE_URI}, ${GITHUB_USER}, ${GIT_SHA} → los reemplaza el pipeline
│   ├── 03-service.yaml
│   └── 04-ingress.yaml         # Ingress clase alb → ALB público
├── infra/                      # Terraform: backend, providers, variables
│   └── modules/
│       ├── networking/         # VPC, subnets públicas (tagueadas para el ALB), IGW, rutas
│       ├── eks/                # Cluster EKS, launch template, managed node group
│       ├── alb_controller/     # AWS Load Balancer Controller (Helm)
│       └── ecr/                # Repositorio ECR
├── .github/workflows/
│   ├── deploy.yaml             # Pipeline: Terraform → Build → Deploy → Validate → Evidence
│   └── destroy.yaml            # Borra todo (manual)
└── README.md
```

## Qué tiene que mostrar la app

Con todo corregido, la URL del ALB responde con datos que solo se pueden obtener corriendo en EKS detrás de un ALB, más la evidencia de tu entrega:

```bash
curl http://<alb>/
# pod=web-app-xxxxxxxxx-xxxxx
# node=ip-10-0-1-23.ec2.internal
# k8s_version=v1.35.x-eks-xxxxxxx       ← API server de EKS
# provider_id=aws:///us-east-1a/i-0abc… ← el nodo es un EC2
# nodegroup=demo-node-group              ← EKS managed node group
# instance_id=i-0abc…                    ← IMDS del EC2
# instance_type=t3.small
# az=us-east-1a
# region=us-east-1
# github_user=<tu-usuario>               ← quien hizo el push
# commit=<sha>                           ← commit desplegado
# alb_trace_id=Root=1-…                  ← el request pasó por un ALB

curl "http://<alb>/?format=json"   # lo mismo en JSON; en el navegador se ve como HTML
curl http://<alb>/health           # ok
```

## Evaluación

La corrección se hace **por la URL del ALB**. El job `Validate` obtiene la URL del ALB y comprueba que la app responda con todos los datos de arriba. Al final de **cada ejecución** (aunque algún job falle), el job `Publish evidence` envía la evidencia al receptor del docente: tu usuario, el commit, el resultado de cada job y, si ya existe, la **URL del ALB**. Así queda registrado cada intento.

El receptor vuelve a validar la URL del ALB por su cuenta y responde con el resultado, que se ve en el log del job y en el resumen de la ejecución en "Actions" (por ejemplo `Intento #7: Entrega OK: la app responde correctamente a traves del ALB.`). Se verifica que:
1. Responda a través del ALB (`/health` devuelve `ok` y `alb_trace_id` no está vacío)
2. La app corre en EKS: `k8s_version` de EKS, `provider_id` de EC2, `nodegroup` e `instance_id` informados
3. Hay 2 réplicas atendiendo (distintos valores de `pod` entre requests)
4. `github_user` es tu usuario y `commit` es el commit de esa ejecución

Recibirás puntos por cada problema que identifiques y corrijas correctamente. No está permitido eliminar ni saltear jobs, pasos o validaciones, ni modificar los jobs `Validate` y `Publish evidence`, ni enviar evidencias a mano, ni hardcodear valores para "pasar" la validación: el docente revisa los cambios de cada commit.

> **Importante:** la URL tiene que estar respondiendo al momento de la corrección. No destruyas la infraestructura ni cierres el laboratorio de AWS Academy hasta que el docente te confirme que tu entrega fue corregida.

## Entrega

1. Clona el repositorio generado
2. En AWS Academy, iniciá el laboratorio y copiá las credenciales (*AWS Details → AWS CLI*)
3. En *Settings → Secrets and variables → Actions*:
   - **Secrets**: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` y `AWS_SESSION_TOKEN`
   - **Variables**: `EVIDENCE_URL` con la URL que te indique el docente
4. Identifica y corrige los problemas, haciendo commit y push a la rama principal con tu propio usuario de GitHub (la evidencia queda a su nombre)
5. Verifica en la pestaña "Actions" que los cinco jobs terminen en verde y abrí la URL del ALB en el navegador

> Las credenciales de AWS Academy expiran al terminar la sesión del laboratorio (~4 h). Si el pipeline falla con `ExpiredToken` o `InvalidClientTokenId` en **todos** los jobs, actualizá los tres secrets.
>
> El state de Terraform se guarda en el bucket `ac2-tfstate-<account-id>`, que el pipeline crea automáticamente en tu cuenta.

## Comandos útiles

Para diagnosticar desde tu máquina necesitás [AWS CLI v2](https://docs.aws.amazon.com/cli/latest/userguide/install-cliv2.html), [kubectl](https://kubernetes.io/docs/tasks/tools/) y, opcionalmente, [Terraform >= 1.5](https://developer.hashicorp.com/terraform/install) y [Docker](https://docs.docker.com/get-docker/). Exportá las credenciales del laboratorio en la terminal y luego:

```bash
# Conectarse al cluster (una vez creado)
aws eks update-kubeconfig --region us-east-1 --name demo-eks

# Estado general de la app
kubectl get deploy,rs,pods,svc,endpoints,ingress -n web-app -o wide
kubectl get events -n web-app --sort-by=.lastTimestamp

# ¿Por qué un pod no arranca o no está Ready?
kubectl describe pod -n web-app <pod>
kubectl logs -n web-app <pod> --previous

# ¿Por qué el Ingress no tiene ALB?
kubectl describe ingress web-app -n web-app
kubectl logs -n kube-system deployment/aws-load-balancer-controller --tail=50

# URL del ALB
kubectl get ingress web-app -n web-app -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

Terraform local (mismo state que el pipeline):

```bash
cd infra
terraform init -backend-config="bucket=ac2-tfstate-$(aws sts get-caller-identity --query Account --output text)"
terraform fmt -recursive
terraform validate
terraform plan
```

> El bucket `ac2-tfstate-<account-id>` lo crea el pipeline (paso *Preparar bucket del state* del job `Terraform`) la primera vez que corre. Si todavía no corrió nunca, el `terraform init` local falla porque el bucket no existe: hacé un push primero o, para usar solo `fmt` y `validate`, inicializá sin backend con `terraform init -backend=false`.

Probar la imagen localmente:

```bash
docker build -t web-app-local ./app
docker run -p 8080:8080 web-app-local
curl http://localhost:8080/health   # fuera de EKS los campos del cluster quedan vacíos
```

## Destruir la infraestructura

Una vez corregida la entrega, ejecutá **Actions → Destroy → Run workflow** escribiendo `destroy`. El workflow borra primero el namespace de la app (para que el controller elimine el ALB) y luego corre `terraform destroy`.

¡Buena suerte!
