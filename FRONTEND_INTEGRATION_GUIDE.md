# Elchi AI Config Generator - Frontend Integration Guide

Bu doküman, Elchi Frontend'e AI-powered Envoy configuration generator özelliğinin entegrasyonunu detaylandırır.

## 🎯 Genel Bakış

### Backend Analizi
- **API Endpoints**: `/api/v3/ai/*` altında 3 endpoint mevcut
- **Tech Stack**: Go, Gin, MongoDB, Claude API
- **Auth**: JWT-based authentication gerekli
- **Response Format**: Consistent JSON structure

### Frontend Analizi  
- **Tech Stack**: React, TypeScript, Ant Design, Redux Toolkit, Axios
- **UI Components**: AntD form components, Monaco editor, drag-drop
- **State Management**: Redux Toolkit + React Query
- **Build Tool**: Vite

## 📋 API Endpoint Detayları

### 1. Get Template
```typescript
GET /api/v3/ai/template
Authorization: Bearer JWT_TOKEN

Response:
{
  template: ConfigRequest,
  description: string,
  endpoints: {...},
  example_usage: string
}
```

### 2. Generate Config
```typescript
POST /api/v3/ai/generate-config
Authorization: Bearer JWT_TOKEN
Content-Type: application/json

Request Body: ConfigRequest
Response: {
  success: true,
  configs: ConfigResponse,
  generated_at: string,
  message: string
}
```

### 3. Apply Configs
```typescript
POST /api/v3/ai/apply-configs
Authorization: Bearer JWT_TOKEN
Content-Type: application/json

Request Body: {
  configs: ConfigResponse,
  apply: {
    listeners: boolean,
    clusters: boolean,
    routes: boolean,
    filters: boolean,
    extensions: boolean,
    endpoints: boolean,
    virtual_hosts: boolean,
    secrets: boolean,
    tls: boolean
  }
}
```

## 🏗️ Frontend Component Tasarımı

### 1. Ana Bileşen: AIConfigGenerator

```typescript
// src/components/dashboard/AIConfigGenerator/index.tsx
import React from 'react';
import { Card, Steps, Button } from 'antd';
import { useState } from 'react';

interface AIConfigGeneratorProps {
  onConfigGenerated?: (config: any) => void;
  onConfigApplied?: (result: any) => void;
}

const AIConfigGenerator: React.FC<AIConfigGeneratorProps> = ({
  onConfigGenerated,
  onConfigApplied
}) => {
  const [currentStep, setCurrentStep] = useState(0);
  const [formData, setFormData] = useState<ConfigRequest>({});
  const [generatedConfig, setGeneratedConfig] = useState<ConfigResponse | null>(null);
  const [loading, setLoading] = useState(false);

  const steps = [
    {
      title: 'Temel Bilgiler',
      content: <BasicInfoForm data={formData} onChange={setFormData} />
    },
    {
      title: 'Özellikler',
      content: <FeaturesForm data={formData} onChange={setFormData} />
    },
    {
      title: 'Upstream Yapılandırması',
      content: <UpstreamForm data={formData} onChange={setFormData} />
    },
    {
      title: 'Güvenlik',
      content: <SecurityForm data={formData} onChange={setFormData} />
    },
    {
      title: 'Performans',
      content: <PerformanceForm data={formData} onChange={setFormData} />
    },
    {
      title: 'Gelişmiş Ayarlar',
      content: <AdvancedForm data={formData} onChange={setFormData} />
    },
    {
      title: 'Önizleme & Uygula',
      content: <PreviewApplyForm 
        config={generatedConfig} 
        onGenerate={handleGenerate}
        onApply={handleApply}
      />
    }
  ];

  return (
    <Card title="AI ile Envoy Konfigürasyonu Oluştur">
      <Steps current={currentStep} items={steps.map(item => ({ title: item.title }))} />
      <div style={{ marginTop: 24 }}>
        {steps[currentStep].content}
      </div>
      {/* Navigation buttons */}
    </Card>
  );
};

export default AIConfigGenerator;
```

### 2. Form Bileşenleri

#### BasicInfoForm
```typescript
// src/components/dashboard/AIConfigGenerator/BasicInfoForm.tsx
import React from 'react';
import { Form, Input, Select, Card } from 'antd';

interface BasicInfoFormProps {
  data: Partial<ConfigRequest>;
  onChange: (data: Partial<ConfigRequest>) => void;
}

const BasicInfoForm: React.FC<BasicInfoFormProps> = ({ data, onChange }) => {
  return (
    <Card title="Temel Servis Bilgileri">
      <Form layout="vertical" initialValues={data} onValuesChange={(_, values) => onChange({...data, ...values})}>
        <Form.Item 
          label="Servis Adı" 
          name="service_name" 
          rules={[{ required: true, message: 'Servis adı gerekli!' }]}
        >
          <Input placeholder="örn: web-frontend" />
        </Form.Item>
        
        <Form.Item label="Açıklama" name="description">
          <Input.TextArea rows={3} placeholder="Servis hakkında kısa açıklama" />
        </Form.Item>
        
        <Form.Item 
          label="Ortam" 
          name="environment" 
          rules={[{ required: true, message: 'Ortam seçimi gerekli!' }]}
        >
          <Select placeholder="Ortam seçiniz">
            <Select.Option value="development">Development</Select.Option>
            <Select.Option value="staging">Staging</Select.Option>
            <Select.Option value="production">Production</Select.Option>
          </Select>
        </Form.Item>
        
        <Form.Item 
          label="Proje" 
          name="project" 
          rules={[{ required: true, message: 'Proje seçimi gerekli!' }]}
        >
          <ProjectSelector />
        </Form.Item>
      </Form>
    </Card>
  );
};
```

#### FeaturesForm
```typescript
// src/components/dashboard/AIConfigGenerator/FeaturesForm.tsx
import React from 'react';
import { Card, Switch, Row, Col, Typography } from 'antd';

const { Text } = Typography;

interface FeaturesFormProps {
  data: Partial<ConfigRequest>;
  onChange: (data: Partial<ConfigRequest>) => void;
}

const FeaturesForm: React.FC<FeaturesFormProps> = ({ data, onChange }) => {
  const handleFeatureChange = (feature: string, value: boolean) => {
    onChange({
      ...data,
      [feature]: value
    });
  };

  const features = [
    { key: 'require_https', label: 'HTTPS Zorunlu', desc: 'HTTP trafiğini HTTPS\'e yönlendir' },
    { key: 'enable_cors', label: 'CORS Desteği', desc: 'Cross-Origin Resource Sharing etkinleştir' },
    { key: 'enable_auth', label: 'Authentication', desc: 'JWT/OAuth authentication etkinleştir' },
    { key: 'enable_rate_limit', label: 'Rate Limiting', desc: 'İstek hız sınırlaması' },
    { key: 'enable_logging', label: 'Access Logging', desc: 'Detaylı erişim logları' },
    { key: 'enable_metrics', label: 'Metrics Collection', desc: 'Prometheus metrics toplama' }
  ];

  return (
    <Card title="Özellik Seçimi">
      <Row gutter={[16, 16]}>
        {features.map(feature => (
          <Col span={12} key={feature.key}>
            <div style={{ 
              border: '1px solid #f0f0f0', 
              borderRadius: 8, 
              padding: 16,
              height: '100%'
            }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                <div>
                  <Text strong>{feature.label}</Text>
                  <br />
                  <Text type="secondary" style={{ fontSize: '12px' }}>
                    {feature.desc}
                  </Text>
                </div>
                <Switch
                  checked={data[feature.key as keyof ConfigRequest] as boolean}
                  onChange={(value) => handleFeatureChange(feature.key, value)}
                />
              </div>
            </div>
          </Col>
        ))}
      </Row>
    </Card>
  );
};
```

#### UpstreamForm
```typescript
// src/components/dashboard/AIConfigGenerator/UpstreamForm.tsx
import React from 'react';
import { Form, Input, Select, InputNumber, Switch, Card } from 'antd';
import { PlusOutlined, MinusCircleOutlined } from '@ant-design/icons';

interface UpstreamFormProps {
  data: Partial<ConfigRequest>;
  onChange: (data: Partial<ConfigRequest>) => void;
}

const UpstreamForm: React.FC<UpstreamFormProps> = ({ data, onChange }) => {
  return (
    <Card title="Upstream Konfigürasyonu">
      <Form
        layout="vertical"
        initialValues={data.upstream}
        onValuesChange={(_, values) => onChange({...data, upstream: values})}
      >
        <Form.List name="hosts" initialValue={['']}>
          {(fields, { add, remove }) => (
            <>
              <Form.Item label="Backend Host'lar">
                {fields.map(({ key, name, ...restField }) => (
                  <div key={key} style={{ display: 'flex', marginBottom: 8 }}>
                    <Form.Item
                      {...restField}
                      name={name}
                      rules={[
                        { required: true, message: 'Host adresi gerekli!' },
                        { pattern: /^[a-zA-Z0-9.-]+$/, message: 'Geçersiz host formatı' }
                      ]}
                      style={{ flex: 1, marginRight: 8 }}
                    >
                      <Input placeholder="örn: backend-service.default.svc" />
                    </Form.Item>
                    {fields.length > 1 && (
                      <MinusCircleOutlined onClick={() => remove(name)} />
                    )}
                  </div>
                ))}
                <Form.Item>
                  <Button
                    type="dashed"
                    onClick={() => add()}
                    block
                    icon={<PlusOutlined />}
                  >
                    Host Ekle
                  </Button>
                </Form.Item>
              </Form.Item>
            </>
          )}
        </Form.List>

        <Form.Item
          label="Port"
          name="port"
          rules={[
            { required: true, message: 'Port gerekli!' },
            { type: 'number', min: 1, max: 65535, message: 'Geçersiz port' }
          ]}
        >
          <InputNumber min={1} max={65535} placeholder="8080" style={{ width: '100%' }} />
        </Form.Item>

        <Form.Item
          label="Protokol"
          name="protocol"
          rules={[{ required: true, message: 'Protokol seçimi gerekli!' }]}
        >
          <Select>
            <Select.Option value="http">HTTP</Select.Option>
            <Select.Option value="https">HTTPS</Select.Option>
            <Select.Option value="grpc">gRPC</Select.Option>
            <Select.Option value="tcp">TCP</Select.Option>
          </Select>
        </Form.Item>

        <Form.Item label="Health Check" name="health_check" valuePropName="checked">
          <Switch />
        </Form.Item>

        <Form.Item label="Load Balancing" name="load_balancing">
          <Select defaultValue="round_robin">
            <Select.Option value="round_robin">Round Robin</Select.Option>
            <Select.Option value="least_request">Least Request</Select.Option>
            <Select.Option value="ring_hash">Ring Hash</Select.Option>
            <Select.Option value="random">Random</Select.Option>
          </Select>
        </Form.Item>
      </Form>
    </Card>
  );
};
```

#### SecurityForm
```typescript
// src/components/dashboard/AIConfigGenerator/SecurityForm.tsx
import React from 'react';
import { Form, Input, Select, Switch, Card, Divider } from 'antd';

interface SecurityFormProps {
  data: Partial<ConfigRequest>;
  onChange: (data: Partial<ConfigRequest>) => void;
  enableAuth: boolean; // Props'tan gelen feature flag
}

const SecurityForm: React.FC<SecurityFormProps> = ({ data, onChange, enableAuth }) => {
  if (!enableAuth) {
    return (
      <Card title="Güvenlik Ayarları">
        <div style={{ textAlign: 'center', padding: '40px 0', color: '#999' }}>
          <p>Güvenlik ayarları için "Authentication" özelliğini etkinleştirin</p>
        </div>
      </Card>
    );
  }

  return (
    <Card title="Güvenlik Ayarları">
      <Form
        layout="vertical"
        initialValues={data.security}
        onValuesChange={(_, values) => onChange({...data, security: values})}
      >
        <Form.Item label="Authentication Türü" name="auth_type">
          <Select defaultValue="jwt">
            <Select.Option value="jwt">JWT Token</Select.Option>
            <Select.Option value="basic">Basic Auth</Select.Option>
            <Select.Option value="oauth2">OAuth2</Select.Option>
          </Select>
        </Form.Item>

        <Form.List name="allowed_origins">
          {(fields, { add, remove }) => (
            <Form.Item label="İzin Verilen Origin'ler">
              {fields.map(({ key, name, ...restField }) => (
                <div key={key} style={{ display: 'flex', marginBottom: 8 }}>
                  <Form.Item
                    {...restField}
                    name={name}
                    rules={[{ required: true, message: 'Origin gerekli!' }]}
                    style={{ flex: 1, marginRight: 8 }}
                  >
                    <Input placeholder="https://example.com" />
                  </Form.Item>
                  {fields.length > 1 && (
                    <MinusCircleOutlined onClick={() => remove(name)} />
                  )}
                </div>
              ))}
              <Button
                type="dashed"
                onClick={() => add()}
                block
                icon={<PlusOutlined />}
              >
                Origin Ekle
              </Button>
            </Form.Item>
          )}
        </Form.List>

        <Divider>TLS Ayarları</Divider>

        <Form.Item label="TLS Etkin" name={['tls', 'enabled']} valuePropName="checked">
          <Switch />
        </Form.Item>

        <Form.Item label="Sertifika Yolu" name={['tls', 'certificate_path']}>
          <Input placeholder="/etc/ssl/certs/service.crt" />
        </Form.Item>

        <Form.Item label="Private Key Yolu" name={['tls', 'key_path']}>
          <Input placeholder="/etc/ssl/private/service.key" />
        </Form.Item>
      </Form>
    </Card>
  );
};
```

### 3. API Service Layer

```typescript
// src/services/aiConfigService.ts
import axios, { AxiosResponse } from 'axios';
import { getAuthToken } from '../utils/auth';

interface AIConfigService {
  getTemplate(): Promise<AxiosResponse<any>>;
  generateConfig(request: ConfigRequest): Promise<AxiosResponse<ConfigResponse>>;
  applyConfigs(configs: ConfigResponse, apply: ApplyConfig): Promise<AxiosResponse<any>>;
}

const aiConfigService: AIConfigService = {
  async getTemplate() {
    return axios.get('/api/v3/ai/template', {
      headers: {
        'Authorization': `Bearer ${getAuthToken()}`,
        'Content-Type': 'application/json',
      }
    });
  },

  async generateConfig(request: ConfigRequest) {
    return axios.post('/api/v3/ai/generate-config', request, {
      headers: {
        'Authorization': `Bearer ${getAuthToken()}`,
        'Content-Type': 'application/json',
      }
    });
  },

  async applyConfigs(configs: ConfigResponse, apply: ApplyConfig) {
    return axios.post('/api/v3/ai/apply-configs', { configs, apply }, {
      headers: {
        'Authorization': `Bearer ${getAuthToken()}`,
        'Content-Type': 'application/json',
      }
    });
  }
};

export default aiConfigService;
```

### 4. Redux State Management

```typescript
// src/redux/slices/aiConfigSlice.ts
import { createSlice, createAsyncThunk, PayloadAction } from '@reduxjs/toolkit';
import aiConfigService from '../../services/aiConfigService';

interface AIConfigState {
  template: any | null;
  generatedConfig: ConfigResponse | null;
  appliedConfigs: any | null;
  loading: boolean;
  error: string | null;
  currentFormData: Partial<ConfigRequest>;
}

const initialState: AIConfigState = {
  template: null,
  generatedConfig: null,
  appliedConfigs: null,
  loading: false,
  error: null,
  currentFormData: {}
};

export const fetchTemplate = createAsyncThunk(
  'aiConfig/fetchTemplate',
  async () => {
    const response = await aiConfigService.getTemplate();
    return response.data;
  }
);

export const generateConfig = createAsyncThunk(
  'aiConfig/generateConfig',
  async (request: ConfigRequest) => {
    const response = await aiConfigService.generateConfig(request);
    return response.data;
  }
);

export const applyConfigs = createAsyncThunk(
  'aiConfig/applyConfigs',
  async ({ configs, apply }: { configs: ConfigResponse; apply: ApplyConfig }) => {
    const response = await aiConfigService.applyConfigs(configs, apply);
    return response.data;
  }
);

const aiConfigSlice = createSlice({
  name: 'aiConfig',
  initialState,
  reducers: {
    updateFormData: (state, action: PayloadAction<Partial<ConfigRequest>>) => {
      state.currentFormData = { ...state.currentFormData, ...action.payload };
    },
    clearGeneratedConfig: (state) => {
      state.generatedConfig = null;
    },
    clearError: (state) => {
      state.error = null;
    }
  },
  extraReducers: (builder) => {
    builder
      .addCase(fetchTemplate.pending, (state) => {
        state.loading = true;
        state.error = null;
      })
      .addCase(fetchTemplate.fulfilled, (state, action) => {
        state.loading = false;
        state.template = action.payload;
      })
      .addCase(fetchTemplate.rejected, (state, action) => {
        state.loading = false;
        state.error = action.error.message || 'Template fetch failed';
      })
      .addCase(generateConfig.pending, (state) => {
        state.loading = true;
        state.error = null;
      })
      .addCase(generateConfig.fulfilled, (state, action) => {
        state.loading = false;
        state.generatedConfig = action.payload.configs;
      })
      .addCase(generateConfig.rejected, (state, action) => {
        state.loading = false;
        state.error = action.error.message || 'Config generation failed';
      })
      .addCase(applyConfigs.pending, (state) => {
        state.loading = true;
        state.error = null;
      })
      .addCase(applyConfigs.fulfilled, (state, action) => {
        state.loading = false;
        state.appliedConfigs = action.payload;
      })
      .addCase(applyConfigs.rejected, (state, action) => {
        state.loading = false;
        state.error = action.error.message || 'Config apply failed';
      });
  }
});

export const { updateFormData, clearGeneratedConfig, clearError } = aiConfigSlice.actions;
export default aiConfigSlice.reducer;
```

### 5. Type Definitions

```typescript
// src/types/aiConfig.ts
export interface ConfigRequest {
  service_name: string;
  description: string;
  environment: string;
  project: string;
  
  require_https: boolean;
  enable_cors: boolean;
  enable_auth: boolean;
  enable_rate_limit: boolean;
  enable_logging: boolean;
  enable_metrics: boolean;
  
  upstream: UpstreamConfig;
  security: SecurityConfig;
  performance: PerformanceConfig;
  custom_filters: string[];
  requirements: string;
}

export interface UpstreamConfig {
  hosts: string[];
  port: number;
  protocol: string;
  health_check: boolean;
  load_balancing: string;
}

export interface SecurityConfig {
  auth_type: string;
  allowed_origins: string[];
  rbac_rules: string[];
  tls: TLSConfig;
}

export interface TLSConfig {
  enabled: boolean;
  certificate_path: string;
  key_path: string;
}

export interface PerformanceConfig {
  rate_limit: RateLimitConfig;
  timeout: TimeoutConfig;
  retry: RetryConfig;
}

export interface RateLimitConfig {
  requests_per_second: number;
  burst_size: number;
}

export interface TimeoutConfig {
  connection_seconds: number;
  request_seconds: number;
}

export interface RetryConfig {
  max_retries: number;
  backoff_ms: number;
}

export interface ConfigResponse {
  listeners: DBResource[];
  clusters: DBResource[];
  routes: DBResource[];
  filters: DBResource[];
  extensions: DBResource[];
  endpoints: DBResource[];
  virtual_hosts: DBResource[];
  secrets: DBResource[];
  tls: DBResource[];
  explanation: string;
  warnings: string[];
}

export interface ApplyConfig {
  listeners: boolean;
  clusters: boolean;
  routes: boolean;
  filters: boolean;
  extensions: boolean;
  endpoints: boolean;
  virtual_hosts: boolean;
  secrets: boolean;
  tls: boolean;
}

export interface DBResource {
  general: GeneralConfig;
  resource: ResourceConfig;
}

export interface GeneralConfig {
  name: string;
  version: string;
  type: string;
  gtype: string;
  project: string;
  collection: string;
  canonical_name: string;
  category: string;
  managed: boolean;
  metadata: Record<string, any>;
  permissions: PermissionsConfig;
  typed_config: TypedConfigItem[];
}

export interface ResourceConfig {
  version: string;
  resource: any;
}

export interface PermissionsConfig {
  users: string[];
  groups: string[];
}

export interface TypedConfigItem {
  name: string;
  canonical_name: string;
  gtype: string;
  type: string;
  category: string;
  collection: string;
  disabled: boolean;
  priority: number;
  parent_name: string;
}
```

## 🚀 Entegrasyon Adımları

### 1. Backend'i Hazırla
- Claude API key'i environment'a ekle: `CLAUDE_API_KEY=your_api_key`
- AI endpoints'lerin çalıştığını test et

### 2. Frontend Geliştirme
1. Type definitions'ı ekle
2. API service layer'ı oluştur
3. Redux slice'ı implement et
4. Form componentleri geliştir
5. Ana AI Config Generator component'ini yaz

### 3. Routing ve Navigasyon
```typescript
// src/Route.tsx'e ekle
{
  path: '/ai-config',
  element: <AIConfigGenerator />,
  name: 'AI Config Generator'
}
```

### 4. Menu'ye Ekle
Dashboard menu'sünde "AI Config Generator" seçeneği ekle.

### 5. Test ve Debug
- API çağrılarını test et
- Form validasyonlarını kontrol et
- Generated config'ları preview et
- Apply işlemini test et

## 🎨 UI/UX Önerileri

### 1. Progressive Disclosure
- Başlangıçta basit form
- Advanced seçenekleri isteğe bağlı göster
- Step-by-step wizard kullan

### 2. Smart Defaults
- Environment'a göre varsayılan değerler
- Popular configuration presets
- Template library

### 3. Real-time Validation
- Field-level validation
- Backend validation error display
- Form completion progress

### 4. Configuration Preview
- Generated config'ları JSON viewer'da göster
- Syntax highlighting
- Collapsible sections

### 5. Apply Selection Interface
- Checkbox'larla hangi config'ların apply edileceğini seç
- Preview before apply
- Rollback option

Bu entegrasyon ile kullanıcılar Elchi UI üzerinden doğal dil ile Envoy configuration'ları oluşturabilir, önizleyebilir ve sisteme uygulayabilirler.