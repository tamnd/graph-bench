# memgraph: the Memgraph benchmark target on the Bolt plane.
#
# Memgraph speaks Bolt, so the same bolt-tagged graph-bench binary drives it with
# no adapter change; the adapter reads MEMGRAPH_URI, MEMGRAPH_USER, MEMGRAPH_PASS.
# The stock image ships with auth off, which is why the adapter defaults the user
# and password to empty.
#
# memgraph-mage carries the MAGE algorithm library, so the analytical workloads
# that call graph procedures (pagerank, weakly connected components) have a
# procedure surface to hit instead of a blank cell.
FROM memgraph/memgraph-mage:3.7.2

# Bolt. Compose maps this to host 7688 so it does not collide with Neo4j on 7687,
# and the adapter is pointed at MEMGRAPH_URI=bolt://host:7688.
EXPOSE 7687
