# Changelog

## [Unreleased] — 奔流 (Honryu)

The project is renamed from Setagaya to **奔流 (Honryu)**, with `honryu` as the
technical slug. The GitHub repository and the Go module path move together —
`github.com/heridotlife/Setagaya` → `github.com/heridotlife/honryu` — so `go get`
breaks once rather than twice. Importers must update their import paths; GitHub
redirects the old repository URL, but Go module paths are not redirected.

**Vocabulary realigned to Taurus and the wider load-testing community.** The
old `plan` / `collection` terms forced users to translate on every interaction:

* `plan` → **scenario** — the reusable workload definition.
* `collection` → **execution** — the runnable unit that groups scenarios.
* the former `execution` config (concurrency, ramp-up, duration, engines) →
  **loadprofile**, whose `Entry` maps onto one Taurus execution block.

This runs through the domain, ports, use-cases, HTTP API (`/api/scenarios`,
`/api/executions`), JSON fields, object-store keys, RBAC resources, Kubernetes
labels, Prometheus metric and label names, and the Grafana dashboards.

* RBAC's previous `execution` resource (lifecycle actions: deploy, trigger,
  stop) is now `run`, since `execution` names the aggregate.
* Environment variables move from the `SETAGAYA_` prefix to `HONRYU_`.
* Prometheus metrics move from `setagaya_*` to `honryu_*`;
  `latency_collection` → `latency_execution` and `latency_plan` →
  `latency_scenario`; the `collection_id` / `plan_id` labels become
  `execution_id` / `scenario_id`. Bundled dashboards are updated to match.
* The database schema is restated as a fresh baseline in the new vocabulary.
  The previous migrations existed so v2 and v3 could share one database during a
  strangler cutover; Honryu migrates from Shibuya by importing JMX assets only,
  so there is no data to carry across.

**Breaking:** every API route, JSON field, environment variable, metric name,
object-store key, and table name changes. There is no compatibility shim.

Honryu requires an **empty database**. Migrations are tracked by filename, and
the baseline renamed all but one file, so pointing Honryu at a pre-rename
database would create the new tables empty alongside the old populated ones and
start up looking healthy but empty. `Migrate` now refuses such a database by
name rather than allowing that. Migrate from Shibuya by importing JMX assets
into a fresh database; uploaded artifacts must be re-uploaded, since the
object-store key prefixes changed with the vocabulary.

## [Unreleased] — v3 rewrite

Ground-up, test-driven rebuild on a hexagonal (ports-and-adapters) architecture,
promoted to the repository root. The legacy v2 codebase (`setagaya/`,
`ingress-controller/`, `local_storage/`) and its Kubernetes/Helm assets have
been removed.

* Pure domain, use-cases over ports, and thin adapters — each adapter validated
  by the same conformance suite as its in-memory fake.
* Repository (MySQL), scheduler (Kubernetes), executors (JMeter, k6), object
  storage (local filesystem, Sonatype Nexus), Prometheus metrics, SSE streaming,
  and usage tracking.
* Pluggable authentication (no-auth, static token, OIDC), config-driven RBAC and
  multi-tenancy with tenant-scoped project access and an audit log.
* CI enforces a ≥90% coverage gate across unit, integration (testcontainers),
  and end-to-end tests.

## [2.0.0-rc.1](https://github.com/heridotlife/honryu/compare/v2.0.0-rc...v2.0.0-rc.1) (2025-12-15)

### 🔐 Security Scanning Fixes

#### ✨ Features
* **Enhanced Secret Scanning**: Event-aware TruffleHog configuration for different GitHub trigger types
* **Improved Dockerfile Linting**: Hadolint configuration file for Alpine package handling
* **Security Workflow Optimization**: Proper commit SHA handling for PR and push events

#### 🛠️ Fixes
* **TruffleHog Configuration**: Fixed "BASE and HEAD commits are the same" error in scheduled scans
  - Pull request scans: Use proper base/head commit SHAs (`github.event.pull_request.base.sha` vs `github.event.pull_request.head.sha`)
  - Push event scans: Use before/after commit SHAs (`github.event.before` vs `github.event.after`)
  - Scheduled/manual scans: Use time-based scanning (7-day window) instead of diff-based approach
* **Dockerfile Linting**: Added `.hadolint.yaml` configuration to handle Alpine package versioning
  - Ignores DL3018 warnings (industry best practice for Alpine packages)
  - Uses SHA-pinned base images for security instead of package version pinning
  - Updated code-quality workflow to use the configuration file

#### 📚 Documentation Updates
* **Technical Specifications**: Enhanced security automation documentation
  - Added detailed security scanning workflow descriptions
  - Updated container security features section
  - Documented automated security tools integration
* **Security Integration**: Updated platform overview to highlight security automation improvements

#### ✅ Validation
* **Go Code Quality**: All Go modules pass linting and build successfully
* **Workflow Validation**: All YAML workflow files validated for syntax correctness
* **Security Configuration**: Hadolint and TruffleHog configurations tested and validated

## [2.0.0-rc](https://github.com/heridotlife/honryu/compare/v1.1.2...v2.0.0-rc) (2025-09-06)

### 🚀 Major Platform Modernization Release Candidate

#### ✨ Features
* **Complete Container Modernization**: Security-hardened Docker builds with Go 1.25.1
* **Multi-Stage Docker Builds**: Alpine 3.22 base images with minimal attack surface
* **JMeter Version Compatibility**: Support for both JMeter 3.3 (legacy) and 5.6.3 (modern)
* **GitHub Actions Security Suite**: Comprehensive security scanning and linting automation
* **Enterprise Documentation**: Complete technical specifications and security policies
* **Security-First Design**: All containers run as non-root user (UID 1001)
* **Auto-Formatting Infrastructure**: Prettier, yamllint with git hooks integration

#### 🔐 Security Enhancements
* **Container Security**: Multi-stage builds with static compilation and security flags
* **Automated Security Scanning**: Gosec, CodeQL, Trivy, secret scanning, SBOM generation
* **Security Policy Framework**: Comprehensive vulnerability disclosure and incident response
* **Continuous Monitoring**: Weekly security scans and dependency auditing
* **License Compliance**: Automated open source license verification

#### 🐳 Container Architecture Updates
* **Modern Dockerfiles**: 5 security-hardened Dockerfiles with Alpine 3.22
* **Version Agnostic Agent**: Dynamic JMeter path detection for version compatibility
* **Static Compilation**: CGO_ENABLED=0 with security linker flags
* **No HEALTHCHECK**: Eliminated OCI format warnings, Kubernetes-native health monitoring

#### 📚 Documentation Overhaul
* **Technical Specifications**: 430-line comprehensive technical documentation
* **Security Documentation**: SECURITY.md with disclosure procedures and best practices
* **AI Coding Guidelines**: Updated development patterns and modernization guidelines
* **JMeter Compatibility Guide**: Clear migration path between JMeter versions
* **Release Security Checklist**: 100+ security validation checkpoints

#### 🛠️ Development Tools & Quality
* **Auto-Formatting**: Prettier integration for YAML, Markdown, JSON, JavaScript
* **YAML Validation**: yamllint with formatter-friendly configuration
* **Git Hooks**: Pre-commit hooks with automatic formatting and validation
* **Development Scripts**: npm-based tool management and setup automation

#### 🤖 CI/CD Automation
* **Security Workflows**: Multi-tool security scanning with automated issue creation
* **Code Quality**: Comprehensive linting, testing, and validation automation
* **PR Validation**: Semantic validation, security impact assessment, coverage requirements
* **Dependency Management**: Automated dependency updates with Dependabot
* **Emergency Response**: Critical security advisory automation and escalation

#### 🔧 Configuration Enhancements
* **Organization Agnostic**: Configurable documentation links for any organization
* **Environment Detection**: Improved local development vs production configuration
* **Storage Flexibility**: Enhanced support for multiple storage backends
* **Security Configuration**: Comprehensive security settings and validation

### 🛠️ Technical Improvements
* **Go 1.25.1**: Latest stable Go version with security updates
* **Kubernetes Compatibility**: Enhanced RBAC and security policies
* **Metrics Pipeline**: Improved real-time metrics aggregation and streaming
* **Error Handling**: Enhanced error types and consistent API responses
* **Database Patterns**: Refined active record pattern with better validation

### 📦 Build System Updates
* **Component Builds**: Improved build.sh script with multiple targets
* **Kind Integration**: Enhanced local development with kind cluster automation
* **Image Management**: Efficient multi-platform builds and deployment
* **Security Scanning**: Integrated security scanning in build pipeline

### 🔄 Migration & Compatibility
* **Backward Compatibility**: Maintains compatibility with existing JMeter 3.3 deployments
* **Version Detection**: Automatic JMeter version detection and path configuration
* **Legacy Support**: Dedicated legacy Dockerfile for JMeter 3.3 environments
* **Smooth Migration**: Clear upgrade path from previous versions

## [1.1.2](https://github.com/heridotlife/honryu/compare/v1.1.1...v1.1.2) (2024-12-16)


### Bug Fixes

* fix metric dashboard repo url ([#131](https://github.com/heridotlife/honryu/issues/131)) ([161c5e6](https://github.com/heridotlife/honryu/commit/161c5e64208dcc5637aaf899d1b81298ee40adc3))

## [1.1.1](https://github.com/heridotlife/honryu/compare/v1.1.0...v1.1.1) (2024-12-16)


### Bug Fixes

* remove logging ([#129](https://github.com/heridotlife/honryu/issues/129)) ([83f9353](https://github.com/heridotlife/honryu/commit/83f93539c5b579ce1448fbfa752e254e7c8a2d8e))

## [1.1.0](https://github.com/heridotlife/honryu/compare/v1.0.0...v1.1.0) (2024-10-01)


### Features

* Enable engine metrics exposing in the agent ([#112](https://github.com/heridotlife/honryu/issues/112)) ([d7d25ad](https://github.com/heridotlife/honryu/commit/d7d25adcb96451bc33d1d536f5b7017a64e1f4ba))

## 1.0.0 (2024-08-30)


### Features

* add prefix to differ from main release ([70a38f5](https://github.com/heridotlife/honryu/commit/70a38f574ad5593c78d77456b6a83f735d62f3e4))
* introduce release please ([9f33ad0](https://github.com/heridotlife/honryu/commit/9f33ad0c7c22d1063b68fc22f7746e1ce748c86f))


### Bug Fixes

* add missing charts ([420cdf9](https://github.com/heridotlife/honryu/commit/420cdf94fa56d13b7bec7ce12dde20d14c1ffc39))
* add missing if ([fc5622c](https://github.com/heridotlife/honryu/commit/fc5622ca1a59ca3dec356039145bac5f6bf15c9c))
* better naming ([12a42de](https://github.com/heridotlife/honryu/commit/12a42de7e83c3e37f0e44a6fff923a5f59e48cfe))
* chart could not be generated due to tagging. Use gh cli directly instead of chart-releaser-action ([#111](https://github.com/heridotlife/honryu/issues/111)) ([8ab71bb](https://github.com/heridotlife/honryu/commit/8ab71bb47ce99c5c4d8e42976bcb277409f1354a))
* only build the image when it is a release ([d8fc0a1](https://github.com/heridotlife/honryu/commit/d8fc0a1496f591d6c9254460010b28e3187bf5d8))
* prevent fork polluting the official release registry ([77906e5](https://github.com/heridotlife/honryu/commit/77906e5140365321eb881d7c1edf2db1a94e1ae9))
* should use release action from googleapis repo ([c38a4bb](https://github.com/heridotlife/honryu/commit/c38a4bb2aaeb172a4d1e44296715d950724f5008))
* wrong tag name ([4b33f75](https://github.com/heridotlife/honryu/commit/4b33f7506cf2863665052650b3744ec8505adf1e))
