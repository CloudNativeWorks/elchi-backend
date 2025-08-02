# Elchi Backend Development Environment

Local geliştirme ortamında multiple controller instance'larını test etmek için hazırlanmış Docker Compose tabanlı development environment.

## 🎯 Amaç

Bu setup, controller forwarding functionality'sini test etmek için 3 adet controller ve 1 adet registry servisi çalıştırır.

## 📋 Gereksinimler

- Docker & Docker Compose
- 8099, 8100, 8101, 9090 portları kullanılabilir olmalı

## 🚀 Hızlı Başlangıç

### 1. Development Environment'ı Başlat

```bash
./scripts/dev-up.sh
```

Bu komut:
- Docker image'larını build eder
- Registry service'i başlatır (port 9090)
- 3 adet Controller başlatır (portlar 8099, 8100, 8101)
- Service health check'lerini bekler

### 2. Servislerin Durumunu Kontrol Et

```bash
docker-compose -f docker-compose.dev.yml ps
```

### 3. Logları İzle

```bash
# Tüm servislerin logları
./scripts/dev-logs.sh all

# Sadece Controller-1 logları
./scripts/dev-logs.sh controller-1

# Sadece Registry logları  
./scripts/dev-logs.sh registry
```

### 4. Forwarding'i Test Et

```bash
./scripts/test-forward.sh
```

### 5. Environment'ı Durdur

```bash
./scripts/dev-down.sh
```

### 6. Tamamen Temizle (images dahil)

```bash
./scripts/dev-clean.sh
```

## 🏗️ Servis Yapısı

```
┌─────────────────┐
│   Registry      │ :9090
│   Service       │
└─────────────────┘
          │
    ┌─────┼─────┐
    │     │     │
    ▼     ▼     ▼
┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│ Controller-1│ │ Controller-2│ │ Controller-3│
│ HTTP: :8099 │ │ HTTP: :8099 │ │ HTTP: :8099 │
│ gRPC: :50051│ │ gRPC: :50051│ | gRPC: :50051│
│ Ext: 8099   │ │ Ext: 8100   │ │ Ext: 8101   │
│      50051  │ │      50052  │ │      50053  │
└─────────────┘ └─────────────┘ └─────────────┘
```

**Port Mapping:**
- **Internal HTTP**: Tüm controller'lar Docker içinde 8099 portunda çalışır
- **Internal gRPC**: Tüm controller'lar Docker içinde 50051 portunda çalışır
- **External HTTP**: Host'tan farklı portlarla erişilir (8099, 8100, 8101)
- **External gRPC**: Host'tan farklı portlarla erişilir (50051, 50052, 50053)
- **Forwarding**: Container'lar hostname ile konuşur (controller-1:8099, controller-2:8099, vs.)

## 🧪 Test Senaryoları

### Manuel Test

```bash
# Controller-1'e istek gönder
curl -X POST http://localhost:8099/api/op/clients \
  -H 'Content-Type: application/json' \
  -d '{
    "command": "deploy", 
    "clients": [
      {
        "clientID": "test-client-1",
        "downstreamAddress": "192.168.1.100"
      }
    ]
  }'
```

### Forwarding Davranışı

1. **Client Lokal İse**: Controller doğrudan işler
2. **Client Başka Controller'da İse**: Registry'den location alır, HTTP forwarding yapar
3. **Client Bulunamaz İse**: Hata döner

## 📊 Log İzleme

Forwarding işlemlerini detaylı görmek için:

```bash
# Controller-1 logları (forwarding gönderen)
./scripts/dev-logs.sh controller-1

# Controller-2 logları (forwarding alan)  
./scripts/dev-logs.sh controller-2

# Registry logları (location service)
./scripts/dev-logs.sh registry
```

## ⚙️ Config Dosyaları

- `.configs/controller-dev-1.yaml` - Controller 1 config (port 8099)
- `.configs/controller-dev-2.yaml` - Controller 2 config (port 8100)  
- `.configs/controller-dev-3.yaml` - Controller 3 config (port 8101)
- `.configs/config-local.yaml` - Registry config (port 9090)

## 🔧 Troubleshooting

### Port Çakışması

```bash
# Hangi process port kullanıyor?
lsof -i :8099
lsof -i :8100
lsof -i :8101
lsof -i :9090
```

### Service Başlamıyor

```bash
# Servislerin durumunu kontrol et
docker-compose -f docker-compose.dev.yml ps

# Build log'larını kontrol et
docker-compose -f docker-compose.dev.yml logs registry
docker-compose -f docker-compose.dev.yml logs controller-1
```

### Database Connection

MongoDB Atlas kullanılıyor. Connection string `.configs/controller-dev-*.yaml` dosyalarında.

## 🛠️ Development Workflow

1. Code değişikliği yap
2. `./scripts/dev-down.sh` - Environment'ı durdur
3. `./scripts/dev-up.sh` - Yeniden başlat
4. `./scripts/test-forward.sh` - Test et
5. `./scripts/dev-logs.sh all` - Logları izle

## 🎛️ Alternative: Native Go Run

Docker kullanmak istemiyorsan (farklı portlarda çalıştırmak için):

```bash
# Terminal 1: Registry
go run main.go elchi-registry --config .configs/config-local.yaml

# Terminal 2: Controller 1 (default port)
go run main.go elchi-controller --config .configs/controller-dev-1.yaml

# Terminal 3: Controller 2 (farklı port için config override)
HTTP_SERVER_PORT=8100 go run main.go elchi-controller --config .configs/controller-dev-2.yaml

# Terminal 4: Controller 3 (farklı port için config override)  
HTTP_SERVER_PORT=8101 go run main.go elchi-controller --config .configs/controller-dev-3.yaml
```

**Not**: Native Go run'da forwarding test etmek için localhost:8099/8100/8101 kullanılır, hostname forwarding çalışmaz.

## 📈 Performance

- **Docker Compose**: Hızlı başlangıç, gerçek production benzeri
- **Native Go**: Daha hızlı rebuild, debug kolaylığı
- **Skaffold**: Hot reload, advanced development (setup gerekli)

## 🔄 Hot Reload için Skaffold (Opsiyonel)

Sürekli development için Skaffold setup'ı da yapılabilir:

```yaml
# skaffold.yaml (örnek)
apiVersion: skaffold/v2beta21
kind: Config
build:
  artifacts:
  - image: elchi-backend
    docker:
      dockerfile: Dockerfile-release
```

Bu setup ile forward functionality'sini K8s environment'a sürekli deploy etmeden test edebilirsin! 🎉 