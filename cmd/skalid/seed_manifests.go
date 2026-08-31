package main

// The seeded manifests. Each project's versions differ in ways a real team
// would iterate on (replicas, resources, a new variable), so consecutive
// deployments produce distinct revisions instead of deduplicating.

const storefrontV1 = `version: "1"
name: storefront
description: Web shop with a public storefront, a JSON API, and a background worker

applications:
  web:
    build:
      context: ./web
    environment:
      API_URL: "https://${APP_DOMAIN}/api"
      SESSION_SECRET: "${SESSION_SECRET}"
      SENTRY_DSN: "${SENTRY_DSN}"
    ports:
      http:
        port: 3000
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /healthz
        interval: 10s
        timeout: 2s
      liveness:
        http:
          port: http
          path: /healthz
        interval: 30s
        timeout: 2s
    resources:
      requests:
        cpu: 0.1
        memory: 128MB
      limits:
        cpu: 0.5
        memory: 512MB
        temporaryStorage: 512MB
    scaling:
      replicas:
        min: 2
        max: 4
      autoscaling:
        cpu:
          targetUtilization: 70

  api:
    build:
      context: ./api
    environment:
      DATABASE_URL: "{{ databases.main.url }}"
      STRIPE_KEY: "${STRIPE_KEY}"
      SESSION_SECRET: "${SESSION_SECRET}"
      S3_ENDPOINT: "{{ buckets.uploads.endpoint }}"
      S3_BUCKET: "{{ buckets.uploads.name }}"
      S3_ACCESS_KEY: "{{ buckets.uploads.access_key }}"
      S3_SECRET_KEY: "{{ buckets.uploads.secret_key }}"
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /api
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /health/ready
        interval: 10s
        timeout: 2s
      liveness:
        http:
          port: http
          path: /health/live
        interval: 30s
        timeout: 2s
    resources:
      requests:
        cpu: 0.25
        memory: 256MB
      limits:
        cpu: 1
        memory: 1GB
        temporaryStorage: 1GB
    scaling:
      replicas:
        min: 2
        max: 6
      autoscaling:
        cpu:
          targetUtilization: 70
    deployment:
      releaseCommand:
        command: ["/app/api", "migrate", "up"]
        timeout: 5m

  worker:
    image: ghcr.io/example/storefront-worker:1.4.2
    environment:
      DATABASE_URL: "{{ databases.main.url }}"
      S3_ENDPOINT: "{{ buckets.uploads.endpoint }}"
      S3_BUCKET: "{{ buckets.uploads.name }}"
      S3_ACCESS_KEY: "{{ buckets.uploads.access_key }}"
      S3_SECRET_KEY: "{{ buckets.uploads.secret_key }}"
    volumes:
      cache:
        mountPath: /var/cache/worker
        size: 5GB
    resources:
      requests:
        cpu: 0.1
        memory: 128MB
      limits:
        cpu: 1
        memory: 512MB
    scaling:
      replicas:
        min: 1

databases:
  main:
    engine: postgres
    version: 17
    storage:
      size: 20GB
    extensions:
      - pg_trgm

buckets:
  uploads:
    quotas:
      storage: 50GB
  assets:
    quotas:
      storage: 10GB
`

const storefrontV2 = `version: "1"
name: storefront
description: Web shop with a public storefront, a JSON API, and a background worker

applications:
  web:
    build:
      context: ./web
    environment:
      API_URL: "https://${APP_DOMAIN}/api"
      SESSION_SECRET: "${SESSION_SECRET}"
      SENTRY_DSN: "${SENTRY_DSN}"
    ports:
      http:
        port: 3000
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /healthz
        interval: 10s
        timeout: 2s
      liveness:
        http:
          port: http
          path: /healthz
        interval: 30s
        timeout: 2s
    resources:
      requests:
        cpu: 0.1
        memory: 128MB
      limits:
        cpu: 0.5
        memory: 512MB
        temporaryStorage: 512MB
    scaling:
      replicas:
        min: 3
        max: 6
      autoscaling:
        cpu:
          targetUtilization: 70

  api:
    build:
      context: ./api
    environment:
      DATABASE_URL: "{{ databases.main.url }}"
      STRIPE_KEY: "${STRIPE_KEY}"
      SESSION_SECRET: "${SESSION_SECRET}"
      S3_ENDPOINT: "{{ buckets.uploads.endpoint }}"
      S3_BUCKET: "{{ buckets.uploads.name }}"
      S3_ACCESS_KEY: "{{ buckets.uploads.access_key }}"
      S3_SECRET_KEY: "{{ buckets.uploads.secret_key }}"
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /api
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /health/ready
        interval: 10s
        timeout: 2s
      liveness:
        http:
          port: http
          path: /health/live
        interval: 30s
        timeout: 2s
    resources:
      requests:
        cpu: 0.25
        memory: 256MB
      limits:
        cpu: 1
        memory: 1GB
        temporaryStorage: 1GB
    scaling:
      replicas:
        min: 3
        max: 8
      autoscaling:
        cpu:
          targetUtilization: 65
    deployment:
      releaseCommand:
        command: ["/app/api", "migrate", "up"]
        timeout: 5m
      rollout:
        strategy: rolling
        maxUnavailable: 0
        maxSurge: 1
        timeout: 10m

  worker:
    image: ghcr.io/example/storefront-worker:1.5.0
    environment:
      DATABASE_URL: "{{ databases.main.url }}"
      S3_ENDPOINT: "{{ buckets.uploads.endpoint }}"
      S3_BUCKET: "{{ buckets.uploads.name }}"
      S3_ACCESS_KEY: "{{ buckets.uploads.access_key }}"
      S3_SECRET_KEY: "{{ buckets.uploads.secret_key }}"
    volumes:
      cache:
        mountPath: /var/cache/worker
        size: 5GB
    resources:
      requests:
        cpu: 0.1
        memory: 128MB
      limits:
        cpu: 1
        memory: 512MB
    scaling:
      replicas:
        min: 1

databases:
  main:
    engine: postgres
    version: 17
    storage:
      size: 20GB
    extensions:
      - pg_trgm

buckets:
  uploads:
    quotas:
      storage: 50GB
  assets:
    quotas:
      storage: 10GB

backups:
  nightly:
    schedule: "0 3 * * *"
    retention: 14d
    include:
      databases: all
      buckets: all
      volumes: all
`

const storefrontV3 = `version: "1"
name: storefront
description: Web shop with a public storefront, a JSON API, and a background worker

applications:
  web:
    build:
      context: ./web
    environment:
      API_URL: "https://${APP_DOMAIN}/api"
      SESSION_SECRET: "${SESSION_SECRET}"
      SENTRY_DSN: "${SENTRY_DSN}"
      FEATURE_CHECKOUT_V2: "true"
    ports:
      http:
        port: 3000
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /healthz
        interval: 10s
        timeout: 2s
      liveness:
        http:
          port: http
          path: /healthz
        interval: 30s
        timeout: 2s
    resources:
      requests:
        cpu: 0.2
        memory: 192MB
      limits:
        cpu: 0.5
        memory: 512MB
        temporaryStorage: 512MB
    scaling:
      replicas:
        min: 3
        max: 6
      autoscaling:
        cpu:
          targetUtilization: 70

  api:
    build:
      context: ./api
    environment:
      DATABASE_URL: "{{ databases.main.url }}"
      STRIPE_KEY: "${STRIPE_KEY}"
      SESSION_SECRET: "${SESSION_SECRET}"
      SENTRY_DSN: "${SENTRY_DSN}"
      S3_ENDPOINT: "{{ buckets.uploads.endpoint }}"
      S3_BUCKET: "{{ buckets.uploads.name }}"
      S3_ACCESS_KEY: "{{ buckets.uploads.access_key }}"
      S3_SECRET_KEY: "{{ buckets.uploads.secret_key }}"
      ASSETS_BUCKET: "{{ buckets.assets.name }}"
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /api
        port: http
        tls: automatic
    health:
      startup:
        http:
          port: http
          path: /health/live
        interval: 2s
        timeout: 2s
        failureThreshold: 30
      readiness:
        http:
          port: http
          path: /health/ready
        interval: 10s
        timeout: 2s
      liveness:
        http:
          port: http
          path: /health/live
        interval: 30s
        timeout: 2s
    resources:
      requests:
        cpu: 0.25
        memory: 256MB
      limits:
        cpu: 1
        memory: 1GB
        temporaryStorage: 1GB
    scaling:
      replicas:
        min: 3
        max: 8
      autoscaling:
        cpu:
          targetUtilization: 65
    placement:
      spread:
        across: nodes
        minimum: 2
        enforcement: preferred
    deployment:
      releaseCommand:
        command: ["/app/api", "migrate", "up"]
        timeout: 5m
      rollout:
        strategy: rolling
        maxUnavailable: 0
        maxSurge: 1
        timeout: 10m
    shutdown:
      gracePeriod: 30s

  worker:
    image: ghcr.io/example/storefront-worker:1.6.1
    environment:
      DATABASE_URL: "{{ databases.main.url }}"
      S3_ENDPOINT: "{{ buckets.uploads.endpoint }}"
      S3_BUCKET: "{{ buckets.uploads.name }}"
      S3_ACCESS_KEY: "{{ buckets.uploads.access_key }}"
      S3_SECRET_KEY: "{{ buckets.uploads.secret_key }}"
    volumes:
      cache:
        mountPath: /var/cache/worker
        size: 10GB
    resources:
      requests:
        cpu: 0.1
        memory: 128MB
      limits:
        cpu: 1
        memory: 512MB
    scaling:
      replicas:
        min: 1

databases:
  main:
    engine: postgres
    version: 17
    storage:
      size: 20GB
    extensions:
      - pg_trgm

buckets:
  uploads:
    quotas:
      storage: 50GB
  assets:
    quotas:
      storage: 10GB

backups:
  nightly:
    schedule: "0 3 * * *"
    retention: 14d
    include:
      databases: all
      buckets: all
      volumes: all
`

const analyticsV1 = `version: "1"
name: analytics
description: Event ingestion, a scheduler, and a reporting dashboard

applications:
  ingest:
    image: ghcr.io/example/analytics-ingest:2.3.0
    environment:
      DATABASE_URL: "{{ databases.events.url }}"
      INGEST_TOKEN: "${INGEST_TOKEN}"
      RAW_BUCKET: "{{ buckets.raw.name }}"
      RAW_ENDPOINT: "{{ buckets.raw.endpoint }}"
      RAW_ACCESS_KEY: "{{ buckets.raw.access_key }}"
      RAW_SECRET_KEY: "{{ buckets.raw.secret_key }}"
    ports:
      http:
        port: 9000
        protocol: http
    routes:
      collect:
        domain: "${APP_DOMAIN}"
        path: /collect
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /ready
        interval: 5s
        timeout: 2s
    resources:
      requests:
        cpu: 0.5
        memory: 512MB
      limits:
        cpu: 2
        memory: 2GB
        temporaryStorage: 4GB
    scaling:
      replicas:
        min: 2
        max: 8
      autoscaling:
        cpu:
          targetUtilization: 60

  dashboard:
    build:
      context: ./dashboard
    environment:
      DATABASE_URL: "{{ databases.events.url }}"
      RETENTION_DAYS: "${RETENTION_DAY}"
    ports:
      http:
        port: 3000
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
    resources:
      requests:
        cpu: 0.1
        memory: 256MB
      limits:
        cpu: 1
        memory: 1GB
    scaling:
      replicas:
        min: 1
        max: 3
      autoscaling:
        cpu:
          targetUtilization: 70

  scheduler:
    build:
      context: ./scheduler
    environment:
      DATABASE_URL: "{{ databases.events.url }}"
      RETENTION_DAYS: "${RETENTION_DAY}"
    resources:
      requests:
        cpu: 0.05
        memory: 64MB
      limits:
        cpu: 0.5
        memory: 256MB
    scaling:
      replicas:
        min: 1

databases:
  events:
    engine: postgres
    version: 17
    storage:
      size: 100GB

buckets:
  raw:
    quotas:
      storage: 500GB
`

const analyticsV2 = `version: "1"
name: analytics
description: Event ingestion, a scheduler, and a reporting dashboard

applications:
  ingest:
    image: ghcr.io/example/analytics-ingest:2.4.1
    environment:
      DATABASE_URL: "{{ databases.events.url }}"
      INGEST_TOKEN: "${INGEST_TOKEN}"
      RAW_BUCKET: "{{ buckets.raw.name }}"
      RAW_ENDPOINT: "{{ buckets.raw.endpoint }}"
      RAW_ACCESS_KEY: "{{ buckets.raw.access_key }}"
      RAW_SECRET_KEY: "{{ buckets.raw.secret_key }}"
      BATCH_SIZE: "500"
    ports:
      http:
        port: 9000
        protocol: http
    routes:
      collect:
        domain: "${APP_DOMAIN}"
        path: /collect
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /ready
        interval: 5s
        timeout: 2s
    resources:
      requests:
        cpu: 0.5
        memory: 768MB
      limits:
        cpu: 2
        memory: 2GB
        temporaryStorage: 4GB
    scaling:
      replicas:
        min: 4
        max: 12
      autoscaling:
        cpu:
          targetUtilization: 60

  dashboard:
    build:
      context: ./dashboard
    environment:
      DATABASE_URL: "{{ databases.events.url }}"
      RETENTION_DAYS: "${RETENTION_DAY}"
    ports:
      http:
        port: 3000
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
    resources:
      requests:
        cpu: 0.1
        memory: 256MB
      limits:
        cpu: 1
        memory: 1GB
    scaling:
      replicas:
        min: 2
        max: 3
      autoscaling:
        cpu:
          targetUtilization: 70

  scheduler:
    build:
      context: ./scheduler
    environment:
      DATABASE_URL: "{{ databases.events.url }}"
      RETENTION_DAYS: "${RETENTION_DAY}"
    resources:
      requests:
        cpu: 0.05
        memory: 64MB
      limits:
        cpu: 0.5
        memory: 256MB
    scaling:
      replicas:
        min: 1

databases:
  events:
    engine: postgres
    version: 17
    storage:
      size: 100GB

buckets:
  raw:
    quotas:
      storage: 500GB
`

const docsV1 = `version: "1"
name: docs
description: Static documentation site served by nginx

applications:
  site:
    image: ghcr.io/example/docs-site:2026.08.1
    ports:
      http:
        port: 80
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /
        interval: 10s
        timeout: 2s
    resources:
      requests:
        cpu: 0.01
        memory: 32MB
      limits:
        cpu: 0.2
        memory: 128MB
    scaling:
      replicas:
        min: 2
`

const mailerV1 = `version: "1"
name: mailer
description: Transactional email API with a delivery queue

applications:
  api:
    build:
      context: .
      dockerfile: Dockerfile.api
    environment:
      DATABASE_URL: "{{ databases.mail.url }}"
      WEBHOOK_KEY: "${WEBHOOK_KEY}"
      DEFAULT_FROM: "${DEFAULT_FROM}"
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN}"
        path: /
        port: http
        tls: automatic
    health:
      readiness:
        http:
          port: http
          path: /healthz
        interval: 10s
        timeout: 2s
    resources:
      requests:
        cpu: 0.1
        memory: 128MB
      limits:
        cpu: 0.5
        memory: 512MB
    scaling:
      replicas:
        min: 2
        max: 4
      autoscaling:
        cpu:
          targetUtilization: 70

  queue:
    build:
      context: .
      dockerfile: Dockerfile.queue
    environment:
      DATABASE_URL: "{{ databases.mail.url }}"
      SMTP_HOST: "${SMTP_HOST}"
      SMTP_USER: "${SMTP_USER}"
      SMTP_PASS: "${SMTP_PASS}"
      DEFAULT_FROM: "${DEFAULT_FROM}"
    volumes:
      spool:
        mountPath: /var/spool/mailer
        size: 2GB
    resources:
      requests:
        cpu: 0.1
        memory: 128MB
      limits:
        cpu: 1
        memory: 512MB
        temporaryStorage: 256MB
    scaling:
      replicas:
        min: 1

databases:
  mail:
    engine: postgres
    version: 17
    storage:
      size: 10GB
`

const sandboxV1 = `version: "1"
name: sandbox
description: Scratch project for trying things out

applications:
  app:
    build:
      context: .
    ports:
      http:
        port: 8080
        protocol: http
    routes:
      public:
        domain: "${APP_DOMAIN:-sandbox.example.com}"
        path: /
        port: http

databases:
  data:
    engine: postgres
    version: 17
`
