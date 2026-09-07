# Use latest stable Grafana with security updates (pinned by digest)
FROM grafana/grafana:13.2.1@sha256:f772d434e8fab0049deb2b1b30abd43342bcfca1537614aa8d36080232cf4283

# Admin credentials are deliberately NOT set here.
#
# They used to be: ENV GF_SECURITY_ADMIN_USER/PASSWORD=honryu, which bakes the
# credential into an image layer -- readable by anyone who can pull the image,
# and permanent, since a later ENV in a running container does not erase the
# layer that shipped it. Grafana admin means datasource edit and arbitrary
# query against Prometheus, so this was a real grant, not a formality.
# (Trivy DS-0031, hadolint DL3064.)
#
# The chart now injects both from the grafana-admin Secret
# (deploy/chart/honryu/templates/grafana-deployment.yaml).
#
# The GF_AUTH_ANONYMOUS_* trio moved to the chart at the same time. Those
# three were never secrets -- "true", "Main Org." and "Viewer" -- but every
# scanner that greps env names for AUTH flagged them, which is 3 of the 4
# DS-0031 criticals this file used to carry. Rather than suppress a rule that
# was right about the fourth, they now live where they belong: whether a
# deployment allows anonymous read is a property of *that deployment*, not of
# the image, and the image's own default is now the safe one (anonymous off).
# See grafana.anonymous.* in the chart's values.yaml.

# Additional security settings
ENV GF_SECURITY_DISABLE_GRAVATAR=true
ENV GF_SECURITY_COOKIE_SECURE=true
ENV GF_SECURITY_COOKIE_SAMESITE=strict
ENV GF_SECURITY_STRICT_TRANSPORT_SECURITY=true
ENV GF_SECURITY_CONTENT_TYPE_PROTECTION=true
ENV GF_SECURITY_X_CONTENT_TYPE_OPTIONS=nosniff
ENV GF_SECURITY_X_XSS_PROTECTION=true
ENV GF_LOG_MODE=console
ENV GF_LOG_LEVEL=info

# Grafana 13's plugin "preinstall" feature reaches out to grafana.com for a
# default catalog (grafana-lokiexplore-app, elasticsearch, zipkin, ...) on
# every cold start. Found live (2026-08-18, phase 16 cutover): this cluster
# has no route to the public internet, so each of the ~6 default plugins
# times out (~10-15s apiece) before Grafana's HTTP listener binds --
# 60-90s+ added to every pod start/restart, and the readiness probe reports
# "connection refused" the whole time. None of these plugins are used by
# this deployment's dashboards.
ENV GF_PLUGINS_PREINSTALL_DISABLED=true

# Copy configuration files with proper permissions
#
# No ./plugins copy: the only vendored plugin was grafana-piechart-panel
# (AngularJS), used by honryu.json's three "Response Status" panels. Angular
# was defaulted off in Grafana 11 and removed in 12 -- unrenderable on this
# pinned 13.1.3 regardless -- so those panels were migrated to Grafana's
# built-in piechart type instead of carrying a dead plugin forward.
COPY --chmod=755 ./provisioning /etc/grafana/provisioning
COPY --chmod=644 ./datasources /etc/grafana/provisioning/datasources
COPY --chmod=644 config.ini /etc/grafana/config.ini
COPY --chmod=644 ./dashboards /var/lib/grafana/dashboards

# Ensure proper ownership and permissions after copying.
#
# /var/lib/grafana/dashboards is included here for the same reason
# /etc/grafana/provisioning is: COPY --chmod applies its one mode to both
# files AND the directory it creates, and 644 on a directory has no execute
# bit -- non-traversable, even by its owner. Found live (2026-08-18): every
# dashboard failed to provision with "lstat .../honryu.json: permission
# denied" until this find pass covered dashboards/ too. Predates this
# phase's panel migration; it is why the dashboards had never actually
# loaded (confirmed via task 14's finding that Grafana had never run here).
# Numeric ids, not names (hadolint DL3066): a non-numeric user cannot be
# resolved by anything reading this image's config without the container's
# own /etc/passwd, and 0/472 are exactly what "root"/"grafana" resolve to
# here -- the same 472 the chart's runAsUser and fsGroup already pin.
USER 0
RUN chown -R 472:0 /etc/grafana/provisioning /var/lib/grafana/dashboards && \
    find /etc/grafana/provisioning /var/lib/grafana/dashboards -type d -exec chmod 755 {} \; && \
    find /etc/grafana/provisioning /var/lib/grafana/dashboards -type f -exec chmod 644 {} \;

# Use non-root grafana user (472, see the DL3066 note above)
USER 472

# Container health check against Grafana's health endpoint (DS-0026).
# Exec form, not shell form (hadolint DL3025): the shell form wraps this in
# /bin/sh -c, which both adds a process and makes the command depend on a
# shell existing in the image. wget's own exit status is already what the
# check wants, so the "|| exit 1" the shell form needed is redundant.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["wget", "--no-verbose", "--tries=1", "--spider", "http://localhost:3000/api/health"]

# Expose Grafana port
EXPOSE 3000
