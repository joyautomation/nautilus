/* matrix_server: the open62541 fixture for nautilus's OPC UA client matrix
 * (opcua/matrix_test.go). Built against a tagged open62541 release by the
 * Dockerfile beside it.
 *
 * Address space, all in namespace "urn:nautilus:matrix:open62541":
 *   Objects/Scalar/<Type>   one writable variable per built-in scalar type
 *                           the driver maps, string ids "Scalar.<Type>"
 *   Objects/Dynamic/Counter Int32, +1 every 100 ms, read-only
 *   Objects/Dynamic/ReadOnly Int32 42, read-only (a write must be refused)
 *
 * Every endpoint the stack offers (None, Basic256Sha256, Aes128/256, both
 * modes) on the given port, with a self-signed certificate made at start,
 * anonymous login allowed, and every client certificate accepted: this is
 * a test fixture, not an example of how to configure a server.
 *
 * Usage: matrix_server [port]   (default 4840)
 */
#include <open62541/plugin/create_certificate.h>
#include <open62541/plugin/log_stdout.h>
#include <open62541/plugin/pki_default.h>
#include <open62541/server.h>
#include <open62541/server_config_default.h>

#include <signal.h>
#include <stdio.h>
#include <stdlib.h>

static volatile UA_Boolean running = true;
static void stopHandler(int sig) { (void)sig; running = false; }

static UA_UInt16 ns;

static UA_NodeId
addFolder(UA_Server *server, const char *name) {
    UA_ObjectAttributes oa = UA_ObjectAttributes_default;
    oa.displayName = UA_LOCALIZEDTEXT("en-US", (char *)(uintptr_t)name);
    UA_NodeId id;
    UA_Server_addObjectNode(server, UA_NODEID_STRING(ns, (char *)(uintptr_t)name),
                            UA_NODEID_NUMERIC(0, UA_NS0ID_OBJECTSFOLDER),
                            UA_NODEID_NUMERIC(0, UA_NS0ID_ORGANIZES),
                            UA_QUALIFIEDNAME(ns, (char *)(uintptr_t)name),
                            UA_NODEID_NUMERIC(0, UA_NS0ID_FOLDERTYPE), oa, NULL, &id);
    return id;
}

static void
addVar(UA_Server *server, UA_NodeId parent, const char *folder, const char *name,
       const UA_DataType *type, const void *value, UA_Boolean writable) {
    char id[64];
    snprintf(id, sizeof id, "%s.%s", folder, name);
    UA_VariableAttributes va = UA_VariableAttributes_default;
    va.displayName = UA_LOCALIZEDTEXT("en-US", (char *)(uintptr_t)name);
    va.dataType = type->typeId;
    va.valueRank = UA_VALUERANK_SCALAR;
    va.accessLevel = UA_ACCESSLEVELMASK_READ | (writable ? UA_ACCESSLEVELMASK_WRITE : 0);
    va.userAccessLevel = va.accessLevel;
    UA_Variant_setScalarCopy(&va.value, value, type);
    UA_NodeId nid = UA_NODEID_STRING_ALLOC(ns, id);
    UA_Server_addVariableNode(server, nid, parent,
                              UA_NODEID_NUMERIC(0, UA_NS0ID_ORGANIZES),
                              UA_QUALIFIEDNAME(ns, (char *)(uintptr_t)name),
                              UA_NODEID_NUMERIC(0, UA_NS0ID_BASEDATAVARIABLETYPE),
                              va, NULL, NULL);
    UA_NodeId_clear(&nid);
    UA_Variant_clear(&va.value); /* the rest of va points at static strings */
}

#define CERT_PATH "/tmp/matrix_cert.der"
#define KEY_PATH "/tmp/matrix_key.der"

static int
readFile(const char *path, UA_ByteString *out) {
    FILE *f = fopen(path, "rb");
    if(!f)
        return 0;
    fseek(f, 0, SEEK_END);
    long n = ftell(f);
    fseek(f, 0, SEEK_SET);
    if(n <= 0 || UA_ByteString_allocBuffer(out, (size_t)n) != UA_STATUSCODE_GOOD) {
        fclose(f);
        return 0;
    }
    size_t got = fread(out->data, 1, (size_t)n, f);
    fclose(f);
    return got == (size_t)n;
}

static void
writeFile(const char *path, const UA_ByteString *b) {
    FILE *f = fopen(path, "wb");
    if(!f)
        return;
    fwrite(b->data, 1, b->length, f);
    fclose(f);
}

static UA_Int32 counter;

static void
tick(UA_Server *server, void *data) {
    (void)data;
    counter++;
    UA_Variant v;
    UA_Variant_setScalar(&v, &counter, &UA_TYPES[UA_TYPES_INT32]);
    UA_Server_writeValue(server, UA_NODEID_STRING(ns, "Dynamic.Counter"), v);
}

int
main(int argc, char **argv) {
    signal(SIGINT, stopHandler);
    signal(SIGTERM, stopHandler);
    UA_UInt16 port = argc > 1 ? (UA_UInt16)atoi(argv[1]) : 4840;

    UA_String subject[3] = {UA_STRING_STATIC("C=US"), UA_STRING_STATIC("O=nautilus"),
                            UA_STRING_STATIC("CN=open62541 matrix fixture")};
    UA_String san[2] = {UA_STRING_STATIC("DNS:localhost"),
                        UA_STRING_STATIC("URI:urn:open62541.server.application")};
    /* The certificate survives `docker restart` (the container's file
     * system does), as a device's does across a power cycle: a client that
     * pinned it on first use must still accept it afterwards. */
    UA_ByteString cert = UA_BYTESTRING_NULL, key = UA_BYTESTRING_NULL;
    UA_StatusCode rv = UA_STATUSCODE_GOOD;
    if(!readFile(CERT_PATH, &cert) || !readFile(KEY_PATH, &key)) {
        UA_ByteString_clear(&cert);
        UA_ByteString_clear(&key);
        rv = UA_CreateCertificate(UA_Log_Stdout, subject, 3, san, 2,
                                  UA_CERTIFICATEFORMAT_DER, NULL, &key, &cert);
        if(rv != UA_STATUSCODE_GOOD) {
            fprintf(stderr, "certificate: %s\n", UA_StatusCode_name(rv));
            return EXIT_FAILURE;
        }
        writeFile(CERT_PATH, &cert);
        writeFile(KEY_PATH, &key);
    }

    UA_Server *server = UA_Server_new();
    UA_ServerConfig *config = UA_Server_getConfig(server);
    rv = UA_ServerConfig_setDefaultWithSecurityPolicies(config, port, &cert, &key,
                                                        NULL, 0, NULL, 0, NULL, 0);
    if(rv != UA_STATUSCODE_GOOD) {
        fprintf(stderr, "config: %s\n", UA_StatusCode_name(rv));
        return EXIT_FAILURE;
    }
    /* No trust list: accept every client certificate. */
    UA_CertificateVerification_AcceptAll(&config->secureChannelPKI);
    UA_CertificateVerification_AcceptAll(&config->sessionPKI);

    ns = UA_Server_addNamespace(server, "urn:nautilus:matrix:open62541");

    UA_NodeId scalar = addFolder(server, "Scalar");
    UA_Boolean b = true;          addVar(server, scalar, "Scalar", "Boolean", &UA_TYPES[UA_TYPES_BOOLEAN], &b, true);
    UA_SByte sb = -8;             addVar(server, scalar, "Scalar", "SByte", &UA_TYPES[UA_TYPES_SBYTE], &sb, true);
    UA_Byte by = 200;             addVar(server, scalar, "Scalar", "Byte", &UA_TYPES[UA_TYPES_BYTE], &by, true);
    UA_Int16 i16 = -1234;         addVar(server, scalar, "Scalar", "Int16", &UA_TYPES[UA_TYPES_INT16], &i16, true);
    UA_UInt16 u16 = 60000;        addVar(server, scalar, "Scalar", "UInt16", &UA_TYPES[UA_TYPES_UINT16], &u16, true);
    UA_Int32 i32 = -123456;       addVar(server, scalar, "Scalar", "Int32", &UA_TYPES[UA_TYPES_INT32], &i32, true);
    UA_UInt32 u32 = 4000000000u;  addVar(server, scalar, "Scalar", "UInt32", &UA_TYPES[UA_TYPES_UINT32], &u32, true);
    UA_Int64 i64 = -1234567890123LL; addVar(server, scalar, "Scalar", "Int64", &UA_TYPES[UA_TYPES_INT64], &i64, true);
    UA_UInt64 u64 = 1234567890123ULL; addVar(server, scalar, "Scalar", "UInt64", &UA_TYPES[UA_TYPES_UINT64], &u64, true);
    UA_Float f = 1.5f;            addVar(server, scalar, "Scalar", "Float", &UA_TYPES[UA_TYPES_FLOAT], &f, true);
    UA_Double d = 2.25;           addVar(server, scalar, "Scalar", "Double", &UA_TYPES[UA_TYPES_DOUBLE], &d, true);
    UA_String s = UA_STRING_STATIC("hello"); addVar(server, scalar, "Scalar", "String", &UA_TYPES[UA_TYPES_STRING], &s, true);
    UA_DateTime dt = UA_DateTime_fromUnixTime(1700000000); addVar(server, scalar, "Scalar", "DateTime", &UA_TYPES[UA_TYPES_DATETIME], &dt, true);

    UA_NodeId dynamic = addFolder(server, "Dynamic");
    addVar(server, dynamic, "Dynamic", "Counter", &UA_TYPES[UA_TYPES_INT32], &counter, false);
    UA_Int32 ro = 42;
    addVar(server, dynamic, "Dynamic", "ReadOnly", &UA_TYPES[UA_TYPES_INT32], &ro, false);
    UA_Server_addRepeatedCallback(server, tick, NULL, 100, NULL);

    rv = UA_Server_run(server, &running);
    UA_Server_delete(server);
    UA_ByteString_clear(&cert);
    UA_ByteString_clear(&key);
    return rv == UA_STATUSCODE_GOOD ? EXIT_SUCCESS : EXIT_FAILURE;
}
