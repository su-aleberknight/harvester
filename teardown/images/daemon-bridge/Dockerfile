FROM registry.suse.com/bci/bci-base:15.5

# Install required packages (iproute2, bash) and clean up
RUN zypper -n refresh && \
    zypper -n install -y iproute2 bash && \
    zypper clean -a

COPY create-bridge.sh /usr/local/bin/create-bridge.sh
RUN chmod +x /usr/local/bin/create-bridge.sh

ENTRYPOINT ["/usr/local/bin/create-bridge.sh"]
