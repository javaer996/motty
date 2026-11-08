# Native methods are looked up by name from JNI.
-keepclasseswithmembernames class dev.motty.app.core.MottyNative {
    native <methods>;
}
