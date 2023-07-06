FROM registry.ci.openshift.org/stolostron/builder:go1.18-linux AS builder

ADD . /opt
WORKDIR /opt

RUN git update-index --refresh; make observatorium

FROM registry.access.redhat.com/ubi8/ubi-minimal:latest as runner

COPY --from=builder /opt/observatorium /bin/observatorium

ARG BUILD_DATE
ARG VERSION
ARG VCS_REF
ARG DOCKERFILE_PATH

RUN microdnf update -y && microdnf clean all

LABEL vendor="Observatorium" \
    name="observatorium/observatorium" \
    description="Observatorium API" \
    io.k8s.display-name="observatorium/observatorium" \
    io.k8s.description="Observatorium API" \
    maintainer="Observatorium <team-monitoring@redhat.com>" \
    version="$VERSION" \
    org.label-schema.build-date=$BUILD_DATE \
    org.label-schema.description="Observatorium API" \
    org.label-schema.docker.cmd="docker run --rm observatorium/observatorium" \
    org.label-schema.docker.dockerfile=$DOCKERFILE_PATH \
    org.label-schema.name="observatorium/observatorium" \
    org.label-schema.schema-version="1.0" \
    org.label-schema.vcs-branch=$VCS_BRANCH \
    org.label-schema.vcs-ref=$VCS_REF \
    org.label-schema.vcs-url="https://github.com/observatorium/observatorium" \
    org.label-schema.vendor="observatorium/observatorium" \
    org.label-schema.version=$VERSION

ENTRYPOINT ["/bin/observatorium"]
