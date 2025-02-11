// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.Common.DependencyInjection;

public static class ServiceOrderHostApplicationBuilderExtensions
{
    /// <summary>
    /// Adds a service descriptor to the IHostApplicationBuilder with a specified priority.
    /// </summary>
    /// <remarks>
    /// If a service descriptor with the same service type already exists, the new service descriptor
    /// will be inserted before the existing one if the new service's priority is higher. Otherwise, it will be added
    /// to the end of the service collection. Services added outside of this method have priority 0.
    /// </remarks>
    public static IHostApplicationBuilder AddServiceWithPriority(this IHostApplicationBuilder builder, ServiceDescriptor serviceDescriptor, int priority)
    {
        builder.Properties[serviceDescriptor] = priority;
        for (int i = 0; i < builder.Services.Count; i++)
        {
            var existingService = builder.Services[i];
            if (existingService.ServiceType == serviceDescriptor.ServiceType)
            {
                var existingPriority = builder.Properties.TryGetValue(existingService, out var value) ? (int)value : 0;
                if (priority > existingPriority)
                {
                    builder.Services.Insert(i, serviceDescriptor);
                    return builder;
                }
            }
        }

        builder.Services.Add(serviceDescriptor);
        return builder;
    }
}
