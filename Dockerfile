FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 \
    libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 \
    libgbm1 libasound2t64 libcups2 libdbus-1-3 libx11-6 libxcb1 \
    libxext6 libxshmfence1 fonts-liberation fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -u 10001 browser && mkdir /data && chown browser:browser /data
COPY dist/linux-amd64/agent-web/bin/agent-web /usr/local/bin/agent-web
USER browser
ENV AGENT_WEB_HOME=/data
VOLUME /data
EXPOSE 9228
ENTRYPOINT ["agent-web"]
CMD ["serve", "--listen", "0.0.0.0:9228"]
