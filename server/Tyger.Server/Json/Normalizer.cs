using System.Reflection;
using System.Runtime.CompilerServices;
using System.Text.Json.Serialization;

namespace Tyger.Server.Json;

public static class Normalizer
{
    /// <summary>
    /// Normalizes "empty" objects to null. Empty strings, empty collections,
    /// and objects with only empty public properties are considered empty.
    /// Properties with the required keyword are not set to null.
    /// Note that this implementation is not efficient.
    /// A better approach would be to use a source generator.
    /// </summary>
    public static T? NormalizeEmptyToNull<T>(T? value) where T : class
    {
        static object? NormalizeImpl(object? value)
        {
            if (value is null)
            {
                return null;
            }

            if (value is string s)
            {
                return s.Length == 0 ? null : value;
            }

            if (value is System.Collections.ICollection c)
            {
                return c.Count == 0 ? null : value;
            }

            if (value is ValueType)
            {
                return value;
            }

            bool hasNonNullProperties = false;

            foreach (var property in value.GetType().GetProperties())
            {
                if (Attribute.IsDefined(property, typeof(CompilerGeneratedAttribute)) ||
                (property.GetCustomAttribute<JsonIgnoreAttribute>() is JsonIgnoreAttribute ignoreAttribute &&
                ignoreAttribute.Condition == JsonIgnoreCondition.Always))
                {
                    continue;
                }

                var propertyValue = property.GetValue(value);
                if (propertyValue == null)
                {
                    continue;
                }

                var normalizedPropertyValue = NormalizeImpl(propertyValue);
                if (normalizedPropertyValue != null || Attribute.IsDefined(property, typeof(RequiredMemberAttribute)))
                {
                    hasNonNullProperties = true;

                    if (normalizedPropertyValue == null || normalizedPropertyValue == propertyValue)
                    {
                        continue;
                    }
                }

                property.SetValue(value, normalizedPropertyValue);
            }

            return hasNonNullProperties ? value : null;
        }

        return (T?)NormalizeImpl(value);
    }
}
