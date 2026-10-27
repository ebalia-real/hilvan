//go:build android

// Puente JNI: la app llama a Core.start/stop/openRoom y el motor llama a Core.onCall.
#include <jni.h>
#include <stdlib.h>
#include <string.h>
#include <android/log.h>
#include "_cgo_export.h"

static JavaVM *jvm;
static jclass core_class;
static jmethodID on_call;

JNIEXPORT jint JNI_OnLoad(JavaVM *vm, void *reserved) {
    JNIEnv *env;
    jvm = vm;
    if ((*vm)->GetEnv(vm, (void **)&env, JNI_VERSION_1_6) != JNI_OK) return JNI_ERR;
    jclass c = (*env)->FindClass(env, "io/github/heelxel/hilvan/Core");
    if (!c) return JNI_ERR;
    core_class = (*env)->NewGlobalRef(env, c);
    on_call = (*env)->GetStaticMethodID(env, core_class, "onCall", "(Ljava/lang/String;Ljava/lang/String;)Ljava/lang/String;");
    return on_call ? JNI_VERSION_1_6 : JNI_ERR;
}

// hv_java_call llama a Core.onCall desde cualquier hilo del motor; devuelve una copia (free).
char *hv_java_call(const char *kind, const char *json) {
    JNIEnv *env;
    int attached = 0;
    if ((*jvm)->GetEnv(jvm, (void **)&env, JNI_VERSION_1_6) == JNI_EDETACHED) {
        if ((*jvm)->AttachCurrentThread(jvm, &env, NULL) != JNI_OK) return NULL;
        attached = 1;
    }
    jstring jk = (*env)->NewStringUTF(env, kind);
    jstring jj = (*env)->NewStringUTF(env, json);
    jstring r = (jstring)(*env)->CallStaticObjectMethod(env, core_class, on_call, jk, jj);
    char *out = NULL;
    if ((*env)->ExceptionCheck(env)) {
        (*env)->ExceptionDescribe(env);
        (*env)->ExceptionClear(env);
    } else if (r) {
        const char *s = (*env)->GetStringUTFChars(env, r, NULL);
        out = strdup(s);
        (*env)->ReleaseStringUTFChars(env, r, s);
        (*env)->DeleteLocalRef(env, r);
    }
    (*env)->DeleteLocalRef(env, jk);
    (*env)->DeleteLocalRef(env, jj);
    if (attached) (*jvm)->DetachCurrentThread(jvm);
    return out;
}

static char *dup_jstring(JNIEnv *env, jstring s) {
    if (!s) return strdup("");
    const char *c = (*env)->GetStringUTFChars(env, s, NULL);
    char *out = strdup(c);
    (*env)->ReleaseStringUTFChars(env, s, c);
    return out;
}

JNIEXPORT jstring JNICALL Java_io_github_heelxel_hilvan_Core_start(JNIEnv *env, jclass cls, jstring data, jstring downloads, jstring model) {
    char *d = dup_jstring(env, data), *l = dup_jstring(env, downloads), *m = dup_jstring(env, model);
    char *url = hvStart(d, l, m);
    free(d); free(l); free(m);
    jstring out = (*env)->NewStringUTF(env, url ? url : "");
    free(url);
    return out;
}

JNIEXPORT void JNICALL Java_io_github_heelxel_hilvan_Core_stop(JNIEnv *env, jclass cls) { hvStop(); }

JNIEXPORT void JNICALL Java_io_github_heelxel_hilvan_Core_openRoom(JNIEnv *env, jclass cls, jstring account, jstring room) {
    char *a = dup_jstring(env, account), *r = dup_jstring(env, room);
    hvOpenRoom(a, r);
    free(a); free(r);
}

JNIEXPORT void JNICALL Java_io_github_heelxel_hilvan_Core_openUri(JNIEnv *env, jclass cls, jstring uri) {
    char *u = dup_jstring(env, uri);
    hvOpenURI(u);
    free(u);
}
