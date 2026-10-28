# Protobuf lite reads its messages' fields by reflection.
-keep class * extends com.google.protobuf.GeneratedMessageLite { *; }
# gomobile's bindings are called from Go by name.
-keep class go.** { *; }
-keep class dev.ironpark.mako.core.** { *; }
