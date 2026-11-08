// JNI adapter for the Go shared library. It lives in the Go package so cgo
// compiles it into libmotty.so alongside the exported Motty* functions.
//
// Payloads cross as byte arrays, never jstring: JNI modified UTF-8 corrupts
// supplementary-plane characters, and Motty carries user text such as host
// labels, key comments and chat messages.

#include <jni.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <limits.h>

#include "_cgo_export.h"

static jbyteArray motty_error(JNIEnv *env) {
    static const char message[] =
        "{\"ok\":false,\"error\":{\"code\":\"bad_request\","
        "\"message\":\"invalid native arguments\"}}";
    jsize length = (jsize)(sizeof(message) - 1);
    jbyteArray out = (*env)->NewByteArray(env, length);
    if (out != NULL) {
        (*env)->SetByteArrayRegion(env, out, 0, length, (const jbyte *)message);
    }
    return out;
}

static jbyteArray motty_result(JNIEnv *env, void *buffer, int length) {
    if (buffer == NULL || length <= 0) {
        MottyFreeBuffer(buffer);
        return motty_error(env);
    }
    jbyteArray out = (*env)->NewByteArray(env, length);
    if (out != NULL) {
        (*env)->SetByteArrayRegion(env, out, 0, length, (const jbyte *)buffer);
    }
    MottyFreeBuffer(buffer);
    return out;
}

JNIEXPORT jbyteArray JNICALL Java_dev_motty_app_core_MottyNative_start(
    JNIEnv *env, jobject thiz,
    jbyteArray dataDir, jbyteArray version, jbyteArray vaultKey) {
    (void)thiz;
    if (dataDir == NULL || version == NULL || vaultKey == NULL) {
        return motty_error(env);
    }
    jsize dataDirLen = (*env)->GetArrayLength(env, dataDir);
    jsize versionLen = (*env)->GetArrayLength(env, version);
    jsize keyLen = (*env)->GetArrayLength(env, vaultKey);
    if (dataDirLen <= 0 || keyLen <= 0) {
        return motty_error(env);
    }

    jbyte *dataDirBytes = (*env)->GetByteArrayElements(env, dataDir, NULL);
    jbyte *versionBytes = (*env)->GetByteArrayElements(env, version, NULL);
    jbyte *keyBytes = (*env)->GetByteArrayElements(env, vaultKey, NULL);
    if (dataDirBytes == NULL || versionBytes == NULL || keyBytes == NULL) {
        if (dataDirBytes != NULL) {
            (*env)->ReleaseByteArrayElements(env, dataDir, dataDirBytes, JNI_ABORT);
        }
        if (versionBytes != NULL) {
            (*env)->ReleaseByteArrayElements(env, version, versionBytes, JNI_ABORT);
        }
        if (keyBytes != NULL) {
            (*env)->ReleaseByteArrayElements(env, vaultKey, keyBytes, JNI_ABORT);
        }
        return motty_error(env);
    }

    int outLen = 0;
    void *result = MottyStartAlloc(dataDirBytes, dataDirLen,
                                   versionBytes, versionLen,
                                   keyBytes, keyLen, &outLen);
    // The Go side copied the key; do not leave the JVM's copy behind.
    memset(keyBytes, 0, (size_t)keyLen);

    (*env)->ReleaseByteArrayElements(env, dataDir, dataDirBytes, JNI_ABORT);
    (*env)->ReleaseByteArrayElements(env, version, versionBytes, JNI_ABORT);
    (*env)->ReleaseByteArrayElements(env, vaultKey, keyBytes, JNI_ABORT);
    return motty_result(env, result, outLen);
}

JNIEXPORT jbyteArray JNICALL Java_dev_motty_app_core_MottyNative_call(
    JNIEnv *env, jobject thiz, jbyteArray method, jbyteArray params) {
    (void)thiz;
    if (method == NULL) {
        return motty_error(env);
    }
    jsize methodLen = (*env)->GetArrayLength(env, method);
    jsize paramsLen = params == NULL ? 0 : (*env)->GetArrayLength(env, params);
    if (methodLen <= 0) {
        return motty_error(env);
    }

    jbyte *methodBytes = (*env)->GetByteArrayElements(env, method, NULL);
    jbyte *paramsBytes = paramsLen > 0
        ? (*env)->GetByteArrayElements(env, params, NULL)
        : NULL;
    if (methodBytes == NULL || (paramsLen > 0 && paramsBytes == NULL)) {
        if (methodBytes != NULL) {
            (*env)->ReleaseByteArrayElements(env, method, methodBytes, JNI_ABORT);
        }
        if (paramsBytes != NULL) {
            (*env)->ReleaseByteArrayElements(env, params, paramsBytes, JNI_ABORT);
        }
        return motty_error(env);
    }

    int outLen = 0;
    void *result = MottyCallAlloc(methodBytes, methodLen, paramsBytes, paramsLen, &outLen);

    (*env)->ReleaseByteArrayElements(env, method, methodBytes, JNI_ABORT);
    if (paramsBytes != NULL) {
        (*env)->ReleaseByteArrayElements(env, params, paramsBytes, JNI_ABORT);
    }
    return motty_result(env, result, outLen);
}

static int motty_buffer(JNIEnv *env, jobject buffer, void **address) {
    if (buffer == NULL) {
        return -1;
    }
    void *pointer = (*env)->GetDirectBufferAddress(env, buffer);
    jlong capacity = (*env)->GetDirectBufferCapacity(env, buffer);
    if (pointer == NULL || capacity <= 0) {
        return -1;
    }
    *address = pointer;
    return capacity > INT_MAX ? INT_MAX : (int)capacity;
}

JNIEXPORT jint JNICALL Java_dev_motty_app_core_MottyNative_pollEvent(
    JNIEnv *env, jobject thiz, jobject buffer) {
    (void)thiz;
    void *address = NULL;
    int capacity = motty_buffer(env, buffer, &address);
    if (capacity < 0) {
        return -2;
    }
    return MottyPollEventBytes(address, capacity);
}

JNIEXPORT jint JNICALL Java_dev_motty_app_core_MottyNative_screenSnapshot(
    JNIEnv *env, jobject thiz, jlong sessionId, jlong sinceVersion, jobject buffer) {
    (void)thiz;
    void *address = NULL;
    int capacity = motty_buffer(env, buffer, &address);
    if (capacity < 0) {
        return -2;
    }
    return MottyScreenSnapshotBytes((uint32_t)sessionId, (unsigned long long)sinceVersion,
                                    address, capacity);
}

JNIEXPORT jint JNICALL Java_dev_motty_app_core_MottyNative_desktopFrame(
    JNIEnv *env, jobject thiz, jlong desktopId, jlong sinceVersion, jobject buffer) {
    (void)thiz;
    void *address = NULL;
    int capacity = motty_buffer(env, buffer, &address);
    if (capacity < 0) {
        return -2;
    }
    return MottyDesktopFrameBytes((uint32_t)desktopId, (unsigned long long)sinceVersion,
                                  address, capacity);
}

JNIEXPORT void JNICALL Java_dev_motty_app_core_MottyNative_stop(
    JNIEnv *env, jobject thiz) {
    (void)env;
    (void)thiz;
    MottyStop();
}
