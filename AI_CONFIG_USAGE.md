# AI-Powered Envoy Configuration Generation

Elchi Backend artık Claude API kullanarak otomatik Envoy konfigürasyonu oluşturabiliyor! Bu özellik kullanıcıların doğal dil ile taleplerini belirtmesine ve AI'ın bu taleplere göre production-ready Envoy config'ları oluşturmasına olanak sağlıyor.

## 🚀 Özellikler

- **Doğal Dil İşleme**: Türkçe veya İngilizce gereksinimlerinizi yazın
- **Akıllı Config Üretimi**: Claude API ile production-ready konfigürasyonlar
- **Validasyon**: Oluşturulan config'lar otomatik doğrulanıyor
- **MongoDB Entegrasyonu**: Generated config'lar direkt MongoDB'ye kayıt ediliyor
- **Multi-Resource Support**: Listener, Cluster, Route, Filter, Virtual Host ve Endpoint desteği

## 📋 Gereksinimler

### Environment Variables
```bash
# Claude API key'inizi ayarlayın
export CLAUDE_API_KEY="your-claude-api-key"
```

### API Endpoints

| Endpoint | Method | Açıklama |
|----------|--------|----------|
| `/api/v3/ai/template` | GET | Config request template'i al |
| `/api/v3/ai/generate-config` | POST | AI ile config oluştur |
| `/api/v3/ai/apply-configs` | POST | Oluşturulan config'ları MongoDB'ye kaydet |

## 🎯 Kullanım Örnekleri

### 1. Basit Web Service Konfigürasyonu

```bash
curl -X POST http://localhost:8099/api/v3/ai/generate-config \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -d '{
    "service_name": "web-frontend",
    "description": "React frontend application",
    "project": "ecommerce",
    "environment": "production",
    "require_https": true,
    "enable_cors": true,
    "upstream": {
      "hosts": ["web-app:3000"],
      "port": 3000,
      "protocol": "http",
      "health_check": true,
      "load_balancing": "round_robin"
    },
    "security": {
      "auth_type": "jwt",
      "allowed_origins": ["https://example.com"]
    },
    "requirements": "HTTPS zorunlu, CORS desteği olan basit web frontend proxy'si istiyorum"
  }'
```

### 2. API Gateway Konfigürasyonu

```bash
curl -X POST http://localhost:8099/api/v3/ai/generate-config \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -d '{
    "service_name": "api-gateway",
    "description": "REST API Gateway with rate limiting",
    "project": "backend",
    "environment": "production",
    "enable_rate_limit": true,
    "enable_auth": true,
    "enable_logging": true,
    "enable_metrics": true,
    "upstream": {
      "hosts": ["api-server-1:8080", "api-server-2:8080"],
      "port": 8080,
      "protocol": "http",
      "health_check": true,
      "load_balancing": "least_request"
    },
    "security": {
      "auth_type": "oauth2",
      "rbac_rules": ["admin:*", "user:read"]
    },
    "performance": {
      "rate_limit": {
        "requests_per_second": 1000,
        "burst_size": 50
      },
      "timeout": {
        "connection": 5,
        "request": 30
      },
      "retry": {
        "max_retries": 3,
        "backoff_ms": 1000
      }
    },
    "requirements": "Yüksek performanslı API gateway. Rate limiting, circuit breaker, retry mekanizması gerekiyor. SQL injection ve XSS koruması da olsun."
  }'
```

### 3. gRPC Service Konfigürasyonu

```bash
curl -X POST http://localhost:8099/api/v3/ai/generate-config \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -d '{
    "service_name": "grpc-service",
    "description": "High-performance gRPC microservice",
    "project": "microservices",
    "environment": "production",
    "enable_metrics": true,
    "enable_logging": true,
    "upstream": {
      "hosts": ["grpc-backend:9090"],
      "port": 9090,
      "protocol": "grpc",
      "health_check": true,
      "load_balancing": "round_robin"
    },
    "security": {
      "tls": {
        "enabled": true,
        "certificate_path": "/etc/ssl/certs/grpc.crt",
        "key_path": "/etc/ssl/private/grpc.key"
      }
    },
    "custom_filters": ["envoy.filters.http.grpc_stats", "envoy.filters.http.grpc_web"],
    "requirements": "mTLS ile güvenli gRPC service. Health check ve metrics collection olsun. gRPC-Web support da gerekiyor browser'lar için."
  }'
```

## 📝 Response Format

AI başarılı bir config ürettiğinde şu formatta response döner:

```json
{
  "success": true,
  "configs": {
    "listeners": [
      {
        "general": {
          "name": "web-frontend-listener",
          "version": "v1.34.2",
          "type": "listener",
          "gtype": "envoy.config.listener.v3.Listener",
          "project": "ecommerce-project-id",
          "collection": "listeners",
          "canonical_name": "config.listener.v3.Listener",
          "category": "listener",
          "managed": true,
          "metadata": {
            "ai_generated": true,
            "from_template": true
          },
          "permissions": {
            "users": [],
            "groups": []
          },
          "typed_config": [
            {
              "name": "http-connection-manager",
              "canonical_name": "envoy.filters.network.http_connection_manager",
              "gtype": "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
              "type": "network_filter",
              "category": "envoy.filters.network",
              "collection": "filters",
              "disabled": false,
              "priority": 0,
              "parent_name": ""
            }
          ]
        },
        "resource": {
          "version": "1",
          "resource": [
            {
              "name": "web-frontend-listener",
              "address": {
                "socket_address": {
                  "address": "0.0.0.0",
                  "port_value": 8080,
                  "protocol": "TCP"
                }
              },
              "filter_chains": [
                {
                  "name": "web-frontend-filter-chain",
                  "filters": [
                    {
                      "name": "web-frontend-hcm-filter",
                      "typed_config": {
                        "type_url": "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
                        "value": "base64_encoded_hcm_config"
                      }
                    }
                  ]
                }
              ]
            }
          ]
        }
      }
    ],
    "clusters": [
      {
        "general": {
          "name": "web-frontend-cluster",
          "version": "v1.34.2",
          "type": "cluster",
          "gtype": "envoy.config.cluster.v3.Cluster",
          "project": "ecommerce-project-id",
          "collection": "clusters",
          "canonical_name": "config.cluster.v3.Cluster",
          "category": "cluster",
          "managed": true,
          "metadata": {
            "ai_generated": true
          },
          "permissions": {
            "users": [],
            "groups": []
          },
          "typed_config": []
        },
        "resource": {
          "version": "1",
          "resource": {
            "name": "web-frontend-cluster",
            "type": "STRICT_DNS",
            "connect_timeout": "5s",
            "load_assignment": {
              "cluster_name": "web-frontend-cluster",
              "endpoints": [
                {
                  "lb_endpoints": [
                    {
                      "endpoint": {
                        "address": {
                          "socket_address": {
                            "protocol": "TCP",
                            "address": "web-app",
                            "port_value": 3000
                          }
                        }
                      }
                    }
                  ]
                }
              ]
            }
          }
        }
      }
    ],
    "extensions": [
      {
        "general": {
          "name": "web-frontend-http-options",
          "version": "v1.34.2",
          "type": "http_protocol_options",
          "gtype": "envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
          "project": "ecommerce-project-id",
          "collection": "extensions",
          "canonical_name": "envoy.upstreams.http.http_protocol_options",
          "category": "envoy.upstreams.http.http_protocol_options",
          "managed": true,
          "metadata": {
            "ai_generated": true
          },
          "permissions": {
            "users": [],
            "groups": []
          }
        },
        "resource": {
          "version": "1",
          "resource": {
            "explicit_http_config": {
              "http2_protocol_options": {
                "connection_keepalive": {
                  "interval": "30s",
                  "timeout": "10s"
                }
              }
            }
          }
        }
      }
    ],
    "explanation": "Web frontend için HTTPS zorunlu, CORS destekli listener ve cluster oluşturuldu. HTTP/2 keepalive ayarları ve JWT authentication ile production-ready konfigürasyon hazırlandı.",
    "warnings": [
      "Production ortamında gerçek TLS sertifikalarını kullanın",
      "Rate limit değerlerini load test sonuçlarına göre fine-tune edin",
      "Base64 encoded filter config'larda gerçek değerleri kontrol edin"
    ]
  },
  "generated_at": "2024-01-01T12:00:00Z",
  "message": "Envoy configurations generated successfully with AI"
}
```

## ✅ Config'ları Uygulama

Oluşturulan config'lar otomatik MongoDB'ye kaydedilmez. Önce preview edip onayladıktan sonra apply etmeniz gerekir:

```bash
curl -X POST http://localhost:8099/api/v3/ai/apply-configs \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -d '{
    "configs": {
      "listeners": [...],
      "clusters": [...],
      "routes": [...],
      "filters": [...],
      "explanation": "...",
      "warnings": [...]
    },
    "apply": {
      "listeners": true,
      "clusters": true,
      "routes": false,
      "filters": true,
      "extensions": true,
      "virtual_hosts": false,
      "endpoints": false,
      "secrets": false,
      "tls": false
    }
  }'
```

## 🎨 Template Alma

Request formatını öğrenmek için template endpoint'ini kullanabilirsiniz:

```bash
curl -X GET http://localhost:8099/api/v3/ai/template \
  -H "Authorization: Bearer YOUR_JWT_TOKEN"
```

## 🔧 Gelişmiş Özellikler

### Custom Filters

```json
{
  "custom_filters": [
    "envoy.filters.http.lua",
    "envoy.filters.http.wasm",
    "envoy.filters.http.fault",
    "envoy.filters.http.ext_authz"
  ],
  "requirements": "Lua script ile custom header manipulation, WASM filter ile advanced processing ve fault injection test için özellikler gerekiyor"
}
```

### A/B Testing

```json
{
  "requirements": "A/B testing için header-based routing. 'x-version' header'ına göre %90 stable, %10 beta versiyona yönlendir. Beta versiyonda hata oranı %5'i geçerse otomatik stable'a dönsün."
}
```

### Multi-Environment Support

```json
{
  "environment": "staging",
  "requirements": "Staging environment için özel ayarlar. Debug logging açık, rate limit daha yüksek, monitoring daha detaylı olsun."
}
```

## 🚨 Güvenlik Notları

1. **Claude API Key**: API key'inizi güvenli bir şekilde saklayın (environment variable)
2. **Config Review**: AI tarafından oluşturulan config'ları production'a almadan önce mutlaka review edin
3. **Resource Limits**: Rate limiting değerlerini sistem kapasitesine göre ayarlayın
4. **TLS Certificates**: Production'da gerçek TLS sertifikalarını kullanın

## 🐛 Troubleshooting

### Claude API Connection Issues
```bash
# API key kontrolü
echo $CLAUDE_API_KEY

# Test request
curl -H "x-api-key: $CLAUDE_API_KEY" https://api.anthropic.com/v1/messages
```

### Config Generation Errors
- Upstream hosts'ların erişilebilir olduğundan emin olun
- Project adının mevcut olduğunu kontrol edin
- Rate limit değerlerinin makul aralıkta olduğunu kontrol edin

### MongoDB Insert Errors
- Collection'ların mevcut olduğunu kontrol edin
- Duplicate name/version/project kombinasyonu hatası alıyorsanız farklı isim kullanın

## 📊 Performance Tips

1. **Batch Operations**: Birden fazla service için config üretirken batch request kullanın
2. **Template Reuse**: Benzer servisler için template'i customize edin
3. **Caching**: Sık kullanılan pattern'lar için config template'leri oluşturun

## 🎓 Örnek Use Case'ler

### E-commerce Platform
```json
{
  "service_name": "checkout-service",
  "requirements": "E-commerce checkout servisi. PCI DSS compliance gerekiyor, SQL injection koruması, session management, fraud detection için custom header'lar"
}
```

### IoT Data Collector
```json
{
  "service_name": "iot-collector", 
  "requirements": "IoT device'lardan gelen verileri toplayan servis. High throughput, message queuing, data validation, device authentication"
}
```

### Media Streaming
```json
{
  "service_name": "video-streaming",
  "requirements": "Video streaming servisi. CDN integration, adaptive bitrate, geographic routing, bandwidth limiting"
}
```

Bu AI özelliği ile Envoy configuration management'ı artık çok daha hızlı ve kolay! 🚀