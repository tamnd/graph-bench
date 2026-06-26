# neo4j: the Neo4j benchmark target on the Bolt plane.
#
# This is the stock Neo4j Community image with one change: the APOC core library
# is enabled, because some of the analytical workloads call apoc procedures the
# bare server does not ship. Auth is set through compose (NEO4J_AUTH); the bolt
# adapter reads NEO4J_USER and NEO4J_PASS to match it.
#
# Pin the tag rather than tracking :latest so a benchmark result names the exact
# server it ran against. Neo4j moved to calendar versioning; 2026.05 is the
# current release line. If you want a long-support pin instead, swap in the LTS
# tag neo4j:5.26-community (latest 5.26.27), which the same config works against.
FROM neo4j:2026.05-community

# Pull APOC core into the plugins directory the image already puts on the path.
ENV NEO4J_PLUGINS='["apoc"]'
# Let apoc procedures run; the default config blocks them.
ENV NEO4J_dbms_security_procedures_unrestricted=apoc.*
ENV NEO4J_dbms_security_procedures_allowlist=apoc.*

# Bolt and HTTP. The adapter points NEO4J_URI at bolt://host:7687.
EXPOSE 7687 7474
